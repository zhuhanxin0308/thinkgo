package cache

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type batchRollbackFaultDriver struct {
	Driver
	injected    bool
	afterCommit bool
	beforeReply func()
	failure     error
}

func (backend *batchRollbackFaultDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return backend.Driver.(AtomicUpdater).Update(key, ttl, update)
}

func (backend *batchRollbackFaultDriver) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	return backend.Driver.(TTLAtomicUpdater).UpdatePreserveTTL(key, update)
}

func (backend *batchRollbackFaultDriver) UpdateWithExpiryContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error)) error {
	if key == "z-fail" && !backend.injected {
		backend.injected = true
		if backend.afterCommit {
			if err := backend.Driver.(ExpiryAtomicUpdater).UpdateWithExpiryContext(ctx, key, ttl, update); err != nil {
				return err
			}
		}
		if backend.beforeReply != nil {
			backend.beforeReply()
		}
		return backend.failure
	}
	return backend.Driver.(ExpiryAtomicUpdater).UpdateWithExpiryContext(ctx, key, ttl, update)
}

func batchRollbackBackends() map[string]func(*testing.T) Driver {
	return map[string]func(*testing.T) Driver{
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
}

// TestBatchRollbackAcrossBackends 验证真实驱动的旧值、永久项、绝对 TTL 和并发新值保护。
func TestBatchRollbackAcrossBackends(t *testing.T) {
	for name, factory := range batchRollbackBackends() {
		for _, mode := range []string{"error", "cancel", "newer", "deleted", "response-lost"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				raw := factory(t)
				fast := NewCache(nil, raw)
				failure := errors.New("批次写入失败")
				backend := &batchRollbackFaultDriver{Driver: raw, failure: failure, afterCommit: mode == "response-lost"}
				slow := NewCache(nil, backend)
				if err := raw.Set("first", "old", time.Minute); err != nil {
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
					backend.beforeReply, backend.failure = cancel, context.Canceled
				}
				if mode == "newer" {
					expected = "newer"
					backend.beforeReply = func() {
						if err := fast.Set("first", expected, 2*time.Minute); err != nil {
							t.Fatal(err)
						}
						expiry = readBatchTestExpiry(t, raw, "first")
					}
				}
				expectedErr := backend.failure
				if mode == "deleted" {
					// 最后一项成功提交后发生删除，批次要在复核水位时发现冲突。
					backend.afterCommit, backend.failure = true, nil
					expectedErr = ErrCacheLockLost
					backend.beforeReply = func() {
						if err := fast.Forget("first"); err != nil {
							t.Fatal(err)
						}
					}
				}
				err := slow.SetManyContext(ctx, map[string]interface{}{"first": "new", "new-key": "created", "permanent": "changed", "z-fail": "failed"}, time.Hour)
				if !errors.Is(err, expectedErr) {
					t.Fatalf("批次错误不符：got=%v want=%v", err, expectedErr)
				}
				if mode == "deleted" {
					if _, found, err := raw.Get("first"); err != nil || found {
						t.Fatalf("回滚重新写入已删除的键：found=%t err=%v", found, err)
					}
				} else {
					if value, found, err := fast.Get("first"); err != nil || !found || value != expected {
						t.Fatalf("回滚破坏目标值: value=%v found=%v err=%v", value, found, err)
					}
					if restored := readBatchTestExpiry(t, raw, "first"); !restored.Equal(expiry) {
						t.Fatalf("回滚改变绝对有效期: got=%v want=%v", restored, expiry)
					}
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

// TestBatchRollbackInterleavedWithForget 验证失效先领票时，失败批次的回滚不会给旧数据换代。
func TestBatchRollbackInterleavedWithForget(t *testing.T) {
	for backend, factory := range batchRollbackBackends() {
		for _, format := range []string{"raw", "envelope"} {
			for _, stage := range []string{"before-publication", "after-publication"} {
				t.Run(backend+"/"+format+"/"+stage, func(t *testing.T) {
					assertBatchRollbackInterleavedWithForget(t, factory(t), format, stage)
				})
			}
		}
	}
}

func assertBatchRollbackInterleavedWithForget(t *testing.T, backend Driver, format, stage string) {
	t.Helper()
	const key = "z-fail"
	reader := NewCache(nil, backend)
	var value interface{} = "原有业务值"
	if format == "envelope" {
		origin, err := reader.nextFenceGeneration(backend)
		if err != nil {
			t.Fatal(err)
		}
		commit, err := reader.nextFenceGeneration(backend)
		if err != nil {
			t.Fatal(err)
		}
		value = newAtomicCacheValueEnvelope(commit, origin, value)
	}
	if err := backend.Set(key, value, time.Minute); err != nil {
		t.Fatal(err)
	}
	original, found, err := backend.Get(key)
	if err != nil || !found {
		t.Fatalf("读取原始快照失败：found=%t err=%v", found, err)
	}
	expiry := readBatchTestExpiry(t, backend, key)
	ackErr := errors.New("批次提交成功但确认丢失")
	batch := NewCache(nil, &batchRollbackFaultDriver{Driver: backend, afterCommit: true, failure: ackErr})

	// Forget 先取得自己的代际。切入点位于发布水位或删除旧值的原子调用之前，均未持有后端键锁。
	invalidator := &reorderedAtomicDriver{Driver: backend, target: key}
	if stage == "before-publication" {
		invalidator.target = reader.keyInvalidationBucketKey(keyInvalidationBucket(key))
	}
	ranBatch := false
	invalidator.before = func() {
		ranBatch = true
		if err := batch.SetMany(map[string]interface{}{key: "批次新值"}, time.Hour); !errors.Is(err, ackErr) {
			t.Fatalf("失败批次未返回确认错误：%v", err)
		}
		restored, found, err := backend.Get(key)
		if err != nil || !found || !reflect.DeepEqual(restored, original) {
			t.Errorf("回滚改变了原始存储表示：got=%#v want=%#v found=%t err=%v", restored, original, found, err)
		}
		if restoredExpiry := readBatchTestExpiry(t, backend, key); !restoredExpiry.Equal(expiry) {
			t.Errorf("回滚改变了原始绝对到期时间：got=%v want=%v", restoredExpiry, expiry)
		}
		got, visible, err := reader.Get(key)
		wantVisible := stage == "before-publication"
		if err != nil || visible != wantVisible || visible && got != "原有业务值" {
			t.Errorf("删除前回滚值的可见性不符：value=%#v found=%t want=%t err=%v", got, visible, wantVisible, err)
		}
	}
	if err := NewCache(nil, invalidator).Forget(key); err != nil || !ranBatch {
		t.Fatalf("删除未完成预期交错：ranBatch=%t err=%v", ranBatch, err)
	}
	values, err := reader.GetMany([]string{key})
	if err != nil || len(values) != 0 {
		t.Fatalf("失效完成后批量读取仍返回旧值：%#v，%v", values, err)
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
