package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/zhuhanxin0308/thinkgo/framework/cache/contract"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
	redisDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver/redis"
)

// reorderedAtomicDriver 在领票后、进入真实后端原子边界前安排另一个合法提交。
type reorderedAtomicDriver struct {
	Driver
	target string
	before func()
	after  func(error)
}

func (d *reorderedAtomicDriver) run(key string, run func() error) error {
	if key == d.target && d.before != nil {
		before := d.before
		d.before = nil
		before()
	}
	err := run()
	if key == d.target && d.after != nil {
		after := d.after
		d.after = nil
		after(err)
	}
	return err
}

func (d *reorderedAtomicDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return d.run(key, func() error { return d.Driver.(AtomicUpdater).Update(key, ttl, update) })
}

func (d *reorderedAtomicDriver) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	return d.run(key, func() error { return d.Driver.(TTLAtomicUpdater).UpdatePreserveTTL(key, update) })
}

func (d *reorderedAtomicDriver) UpdatePreserveTTLConditionally(key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	return d.run(key, func() error { return atomicUpdateCacheValueConditionalTTL(d.Driver, key, update) })
}

// TestAtomicMutationsRebaseOutOfOrderReservations 区分读取最新值的原子合并与已准备旧快照的 Set。
func TestAtomicMutationsRebaseOutOfOrderReservations(t *testing.T) {
	for _, kind := range []string{"memory", "file", "redis"} {
		t.Run(kind, func(t *testing.T) {
			var backend Driver
			switch kind {
			case "memory":
				backend = cacheDriver.NewMemory()
			case "file":
				file, err := cacheDriver.NewFile(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				backend = file
			case "redis":
				backend = atomicOrderRedis(t)
			}
			for _, operation := range []string{"update", "increment", "decrement", "remove", "set"} {
				t.Run(operation, func(t *testing.T) { assertAtomicReservationOrder(t, backend, operation) })
			}
		})
	}
}

func atomicOrderRedis(t *testing.T) Driver {
	backend, _ := atomicOrderRedisWithClock(t)
	return backend
}

func atomicOrderRedisWithClock(t *testing.T) (Driver, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	backend, err := redisDriver.NewRedis(map[string]interface{}{"host": server.Host(), "port": server.Server().Addr().Port, "prefix": "atomic-order:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend, server
}

func assertAtomicReservationOrder(t *testing.T, backend Driver, operation string) {
	t.Helper()
	const key = "atomic-order"
	fast := NewCache(nil, backend)
	wrapped := &reorderedAtomicDriver{Driver: backend, target: key}
	slow := NewCache(nil, wrapped)
	wrapped.before = func() {
		if err := fast.Set(key, int64(10), time.Minute); err != nil {
			t.Fatalf("先执行的较新写入失败: %v", err)
		}
	}
	calls := 0
	var err error
	expected := int64(11)
	switch operation {
	case "update", "remove":
		err = slow.Update(key, time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
			calls++
			if !found || value != int64(10) {
				t.Errorf("原子回调没有读取先提交的新值: value=%v found=%t", value, found)
			}
			return int64(11), operation == "remove", nil
		})
	case "increment":
		_, err = slow.Inc(key, 1)
	case "decrement":
		expected = 9
		_, err = slow.Dec(key, 1)
	case "set":
		expected = 10
		err = slow.Set(key, int64(1), time.Minute)
	}
	if operation == "set" {
		if !errors.Is(err, ErrCacheLockLost) {
			t.Fatalf("旧快照 Set 必须继续拒绝覆盖较新写入: %v", err)
		}
	} else if err != nil {
		t.Fatalf("合法原子操作不能因预留代际乱序失败: %v", err)
	}
	if (operation == "update" || operation == "remove") && calls != 1 {
		t.Fatalf("票据过时的重入重复执行了业务回调: %d", calls)
	}
	value, found, getErr := fast.Get(key)
	if getErr != nil || found != (operation != "remove") || found && value != expected {
		t.Fatalf("原子操作结果错误: value=%v found=%t err=%v", value, found, getErr)
	}
}

// TestAtomicRebaseCannotCrossInvalidation 验证重新领票只能继承真实的新值，不能复活缺失或已失效的值。
func TestAtomicRebaseCannotCrossInvalidation(t *testing.T) {
	for _, operation := range []string{"update", "increment"} {
		for _, base := range []string{"new", "missing", "invalidated"} {
			t.Run(operation+"/"+base, func(t *testing.T) {
				backend := cacheDriver.NewMemory()
				fast := NewCache(nil, backend)
				wrapped := &reorderedAtomicDriver{Driver: backend, target: "session:revoked"}
				slow := NewCache(nil, wrapped)
				wrapped.before = func() {
					if err := fast.Set(wrapped.target, int64(10)); err != nil {
						t.Fatal(err)
					}
				}
				wrapped.after = func(err error) {
					if err == nil {
						t.Fatal("测试未形成过时票据")
					}
					if err = fast.Forget(wrapped.target); err != nil {
						t.Fatal(err)
					}
					if base == "new" {
						err = fast.Set(wrapped.target, int64(20))
					} else if base == "invalidated" {
						err = backend.Set(wrapped.target, newCacheValueEnvelope(1, int64(20)), 0)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if operation == "update" {
					err = slow.Update(wrapped.target, time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
						if base != "new" || !found || value != int64(20) {
							t.Errorf("只允许从最新有效基值继续合并: base=%s value=%v found=%t", base, value, found)
						}
						return int64(21), false, nil
					})
				} else {
					_, err = slow.Inc(wrapped.target, 1)
				}
				if base == "new" && err != nil || base != "new" && !errors.Is(err, ErrCacheLockLost) {
					t.Fatalf("重新领票跨越了真实失效: %v", err)
				}
				if value, found, err := fast.Get(wrapped.target); err != nil || found != (base == "new") || found && value != int64(21) {
					t.Fatalf("失效后的新值受旧操作影响: %v %t %v", value, found, err)
				}
			})
		}
	}
}

// TestAtomicRebaseKeepsUniqueCommitIdentity 验证先前批次的精确回滚不会误删借其值完成的新合并。
func TestAtomicRebaseKeepsUniqueCommitIdentity(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	wrapped := &reorderedAtomicDriver{Driver: backend, target: "first"}
	slow := NewCache(nil, wrapped)
	var batchGeneration uint64
	wrapped.before = func() {
		var err error
		batchGeneration, err = manager.nextFenceGeneration(backend)
		if err != nil {
			t.Fatal(err)
		}
		if err = manager.commitFencedValueContext(context.Background(), backend, wrapped.target, int64(10), time.Minute, batchGeneration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := slow.Inc(wrapped.target, 1); err != nil {
		t.Fatal(err)
	}
	if err := manager.deleteExactGenerationContext(context.Background(), backend, wrapped.target, batchGeneration); err != nil {
		t.Fatal(err)
	}
	if value, found, err := manager.Get(wrapped.target); err != nil || !found || value != int64(11) {
		t.Fatalf("批次回滚误删了随后完成的原子合并: %v %t %v", value, found, err)
	}
}

// TestAtomicRebaseAfterRedisWatchConflict 验证乐观事务未提交的回调允许按原有契约重放。
func TestAtomicRebaseAfterRedisWatchConflict(t *testing.T) {
	assertAtomicWatchConflict(t, atomicOrderRedis(t))
}

func assertAtomicWatchConflict(t *testing.T, backend Driver) {
	t.Helper()
	const key = "watch-order"
	manager := NewCache(nil, backend)
	if err := manager.Set(key, int64(1)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := manager.Update(key, time.Minute, func(value interface{}, found bool) (interface{}, bool, error) {
		calls++
		if !found {
			return nil, false, errors.New("WATCH 重试未读取已存在的值")
		}
		if calls == 1 {
			if err := manager.Set(key, int64(10)); err != nil {
				return nil, false, err
			}
		}
		return value.(int64) + 1, false, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("WATCH 冲突后的合法重入失败: calls=%d err=%v", calls, err)
	}
	if value, found, err := manager.Get(key); err != nil || !found || value != int64(11) {
		t.Fatalf("WATCH 合并丢失了新值: %v %t %v", value, found, err)
	}
}

// TestAtomicRebaseStopsOnContentionOrCancellation 验证持续竞争不形成无界循环，并保留请求取消。
func TestAtomicRebaseStopsOnContentionOrCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded", true: "canceled"}[cancelRequest], func(t *testing.T) {
			backend := cacheDriver.NewMemory()
			fast := NewCache(nil, backend)
			wrapped := &reorderedAtomicDriver{Driver: backend, target: "contended"}
			manager := NewCache(nil, wrapped)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			collisions := 0
			const permittedAttempts = 64
			var displace func()
			displace = func() {
				collisions++
				if err := fast.Set(wrapped.target, int64(10)); err != nil {
					t.Fatal(err)
				}
				if cancelRequest {
					cancel()
				} else if collisions <= permittedAttempts {
					wrapped.before = displace
				}
			}
			wrapped.before = displace
			err := manager.UpdateContext(ctx, wrapped.target, time.Minute, func(interface{}, bool) (interface{}, bool, error) {
				t.Error("持续冲突的原子操作不应执行业务回调")
				return int64(11), false, nil
			})
			if cancelRequest {
				if !errors.Is(err, context.Canceled) || collisions != 1 {
					t.Fatalf("重入未响应取消: %v collisions=%d", err, collisions)
				}
			} else if !errors.Is(err, contract.ErrAtomicUpdateConflict) || collisions != permittedAttempts {
				t.Fatalf("持续竞争未按预算返回冲突: %v collisions=%d", err, collisions)
			}
		})
	}
}
