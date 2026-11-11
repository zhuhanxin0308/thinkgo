package cache

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type batchRollbackFaultDriver struct {
	Driver
	failed      bool
	afterCommit bool
	beforeFail  func()
	failure     error
}

func (backend *batchRollbackFaultDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return backend.Driver.(AtomicUpdater).Update(key, ttl, update)
}

func (backend *batchRollbackFaultDriver) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	return backend.Driver.(TTLAtomicUpdater).UpdatePreserveTTL(key, update)
}

func (backend *batchRollbackFaultDriver) UpdateWithExpiryContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error)) error {
	if key == "z-fail" && !backend.failed {
		backend.failed = true
		if backend.afterCommit {
			if err := backend.Driver.(ExpiryAtomicUpdater).UpdateWithExpiryContext(ctx, key, ttl, update); err != nil {
				return err
			}
		}
		if backend.beforeFail != nil {
			backend.beforeFail()
		}
		return backend.failure
	}
	return backend.Driver.(ExpiryAtomicUpdater).UpdateWithExpiryContext(ctx, key, ttl, update)
}

// TestBatchRollbackAcrossBackends 验证真实驱动的旧值、永久项、绝对 TTL 和并发新值保护。
func TestBatchRollbackAcrossBackends(t *testing.T) {
	factories := map[string]func(*testing.T) Driver{
		"memory": func(*testing.T) Driver { return cacheDriver.NewMemory() },
		"file": func(t *testing.T) Driver {
			backend, err := cacheDriver.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return backend
		},
		"redis": func(t *testing.T) Driver {
			backend, server := atomicOrderRedisWithClock(t)
			// miniredis 不会随墙钟扣减 TTL；固定其服务端时钟，避免测试制造虚假的绝对时刻漂移。
			server.SetTime(time.Now())
			return backend
		},
	}
	for name, factory := range factories {
		for _, mode := range []string{"error", "cancel", "newer", "response-lost"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				raw := factory(t)
				fast := NewCache(nil, raw)
				failure := errors.New("批次写入失败")
				backend := &batchRollbackFaultDriver{Driver: raw, failure: failure, afterCommit: mode == "response-lost"}
				slow := NewCache(nil, backend)
				if err := fast.Set("first", "old", time.Minute); err != nil {
					t.Fatal(err)
				}
				if err := fast.Set("permanent", "keep"); err != nil {
					t.Fatal(err)
				}
				expiry := readBatchTestExpiry(t, raw, "first")
				if expiry.IsZero() {
					t.Fatal("测试旧值必须带有效期")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				expected := "old"
				if mode == "cancel" {
					backend.beforeFail, backend.failure = cancel, context.Canceled
				}
				if mode == "newer" {
					expected = "newer"
					backend.beforeFail = func() {
						if err := fast.Set("first", expected, 2*time.Minute); err != nil {
							t.Fatal(err)
						}
						expiry = readBatchTestExpiry(t, raw, "first")
					}
				}
				err := slow.SetManyContext(ctx, map[string]interface{}{"first": "new", "new-key": "created", "permanent": "changed", "z-fail": "failed"}, time.Hour)
				if !errors.Is(err, backend.failure) {
					t.Fatalf("未返回原始失败: %v", err)
				}
				if value, found, err := fast.Get("first"); err != nil || !found || value != expected {
					t.Fatalf("回滚破坏目标值: value=%v found=%v err=%v", value, found, err)
				}
				if restored := readBatchTestExpiry(t, raw, "first"); !restored.Equal(expiry) {
					t.Fatalf("回滚改变绝对有效期: got=%v want=%v", restored, expiry)
				}
				if value, found, err := fast.Get("permanent"); err != nil || !found || value != "keep" || !readBatchTestExpiry(t, raw, "permanent").IsZero() {
					t.Fatalf("永久旧值未恢复: value=%v found=%v err=%v", value, found, err)
				}
				for _, key := range []string{"new-key", "z-fail"} {
					if _, found, err := fast.Get(key); err != nil || found {
						t.Fatalf("失败批次残留新键 %s: found=%v err=%v", key, found, err)
					}
				}
			})
		}
	}
}

func readBatchTestExpiry(t *testing.T, backend Driver, key string) time.Time {
	t.Helper()
	var result time.Time
	stop := errors.New("只读快照，不提交")
	err := backend.(ExpiryAtomicUpdater).UpdateWithExpiryContext(context.Background(), key, 0, func(_ interface{}, found bool, expiry time.Time) (interface{}, bool, *time.Time, error) {
		if !found {
			t.Errorf("读取过期时间时键 %s 未命中", key)
		}
		result = expiry
		return nil, false, nil, stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	return result
}

// TestBatchFailureRestoresOriginalValueAndExpiry 验证失败批次恢复旧值，且不会重新开始原 TTL。
func TestBatchFailureRestoresOriginalValueAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const originalTTL = time.Minute
		writeErr := errors.New("第二项写入失败")
		backend := &failingFencedBatchDriver{Memory: cacheDriver.NewMemory(), failKey: "second", err: writeErr}
		manager := NewCache(nil, backend)
		if err := manager.Set("first", "old", originalTTL); err != nil {
			t.Fatal(err)
		}
		time.Sleep(originalTTL / 2)
		if err := manager.SetMany(map[string]interface{}{"first": "new", "new-key": "created", "second": "failed"}, time.Hour); !errors.Is(err, writeErr) {
			t.Fatalf("未传播失败原因: %v", err)
		}
		if value, found, err := manager.Get("first"); err != nil || !found || value != "old" {
			t.Fatalf("旧值未恢复: value=%v found=%v err=%v", value, found, err)
		}
		if _, found, err := manager.Get("new-key"); err != nil || found {
			t.Fatalf("失败批次新键未清理: found=%v err=%v", found, err)
		}
		time.Sleep(originalTTL / 2)
		if _, found, err := manager.Get("first"); err != nil || found {
			t.Fatalf("回滚延长了原 TTL: found=%v err=%v", found, err)
		}
	})
}
