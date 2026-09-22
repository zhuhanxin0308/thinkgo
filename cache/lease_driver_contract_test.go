package cache

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	redisDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/redis"
)

// TestLeaseCommitBackendContracts 验证内置后端与命名空间包装的 owner 校验、失败原子性和删除语义。
func TestLeaseCommitBackendContracts(t *testing.T) {
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
			server := miniredis.RunT(t)
			port, err := strconv.Atoi(server.Port())
			if err != nil {
				t.Fatal(err)
			}
			backend, err := redisDriver.NewRedis(map[string]interface{}{"host": server.Host(), "port": port, "prefix": "lease-test:"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = backend.Close() })
			return backend
		},
	}
	for name, factory := range factories {
		for _, namespaced := range []bool{false, true} {
			t.Run(name+"/namespace="+strconv.FormatBool(namespaced), func(t *testing.T) {
				backend := factory(t)
				if namespaced {
					wrapped, err := NewNamespaceDriver(backend, "business:", "tags:")
					if err != nil {
						t.Fatal(err)
					}
					backend = wrapped
				}
				locker, updater := backend.(DistributedLocker), backend.(LockGuardedUpdater)
				const key, lockKey, owner = "value", "lease", "first-owner"
				const ttl = time.Minute
				if ok, err := locker.AcquireLock(lockKey, owner, ttl); err != nil || !ok {
					t.Fatalf("获取锁失败: %v", err)
				}
				called := false
				write := func(interface{}, bool) (interface{}, bool, error) { called = true; return "new", false, nil }
				if ok, err := updater.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, "wrong-owner", write); err != nil || ok || called {
					t.Fatalf("错误 owner 执行了写入: ok=%t called=%t err=%v", ok, called, err)
				}
				if ok, err := updater.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, write); err != nil || !ok {
					t.Fatalf("有效提交失败: %v", err)
				}
				wantErr := errors.New("测试回调失败")
				if ok, err := updater.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { return "bad", false, wantErr }); ok || !errors.Is(err, wantErr) {
					t.Fatalf("回调失败未原子回退: ok=%t err=%v", ok, err)
				}
				if value, found, err := backend.Get(key); err != nil || !found || value != "new" {
					t.Fatalf("已提交值遭破坏: %v %t %v", value, found, err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if ok, err := updater.UpdateIfLockOwnerContext(ctx, key, ttl, lockKey, owner, write); ok || !errors.Is(err, context.Canceled) {
					t.Fatalf("取消未传播: %v", err)
				}
				if ok, err := updater.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); err != nil || !ok {
					t.Fatalf("删除失败: %v", err)
				}
				if _, found, err := backend.Get(key); err != nil || found {
					t.Fatalf("删除未生效: %t %v", found, err)
				}
				if _, err := locker.ReleaseLock(lockKey, owner); err != nil {
					t.Fatal(err)
				}
				called = false
				if ok, err := updater.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, write); err != nil || ok || called {
					t.Fatalf("已释放的锁仍可写入: %t %v", ok, err)
				}
			})
		}
	}
}
