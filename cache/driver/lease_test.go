package driver

import (
	"context"
	"errors"
	"testing"
	"time"
)

type guardedDriver interface {
	Get(string) (interface{}, bool, error)
	AcquireLock(string, string, time.Duration) (bool, error)
	ReleaseLock(string, string) (bool, error)
	UpdateIfLockOwnerContext(context.Context, string, time.Duration, string, string, func(interface{}, bool) (interface{}, bool, error)) (bool, error)
}

// TestLocalLeaseCommitBoundaries 验证本地驱动的无效输入、过期、回调失败及已释放租约均不产生新写入。
func TestLocalLeaseCommitBoundaries(t *testing.T) {
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			var backend guardedDriver = NewMemory()
			if name == "file" {
				file, err := NewFile(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				backend = file
			}
			const key, lockKey, owner = "value", "lease", "owner"
			const ttl = time.Minute
			write := func(interface{}, bool) (interface{}, bool, error) { return "new", false, nil }
			if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, write); err != nil || ok {
				t.Fatalf("缺失租约仍然提交: %t %v", ok, err)
			}
			//lint:ignore SA1012 故意传入空上下文，验证驱动拒绝非法调用且不产生写入。
			if _, err := backend.UpdateIfLockOwnerContext(nil, key, ttl, lockKey, owner, write); err == nil {
				t.Fatal("空上下文未拒绝")
			}
			if _, err := backend.UpdateIfLockOwnerContext(t.Context(), key, -time.Second, lockKey, owner, write); !errors.Is(err, ErrInvalidDriverTTL) {
				t.Fatalf("负 TTL 未拒绝: %v", err)
			}
			if _, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, nil); !errors.Is(err, ErrNilAtomicUpdate) {
				t.Fatalf("空回调未拒绝: %v", err)
			}
			if ok, err := backend.AcquireLock(lockKey, owner, ttl); err != nil || !ok {
				t.Fatal(err)
			}
			if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, "other", write); err != nil || ok {
				t.Fatalf("错误 owner 提交: %t %v", ok, err)
			}
			if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, write); err != nil || !ok {
				t.Fatalf("有效提交失败: %v", err)
			}
			callbackErr := errors.New("回调失败")
			if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { return "bad", false, callbackErr }); ok || !errors.Is(err, callbackErr) {
				t.Fatalf("回调错误未保留: %v", err)
			}
			if value, found, err := backend.Get(key); err != nil || !found || value != "new" {
				t.Fatalf("失败写入破坏旧值: %v %t %v", value, found, err)
			}
			if _, err := backend.UpdateIfLockOwnerContext(t.Context(), key, 0, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); err != nil {
				t.Fatal(err)
			}
			if _, found, err := backend.Get(key); err != nil || found {
				t.Fatalf("删除未生效: %t %v", found, err)
			}
			if _, err := backend.ReleaseLock(lockKey, owner); err != nil {
				t.Fatal(err)
			}
			if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, write); err != nil || ok {
				t.Fatalf("释放后仍可写入: %t %v", ok, err)
			}
			if ok, err := backend.AcquireLock(lockKey, "expiring", time.Nanosecond); err != nil || !ok {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
			called := false
			if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, "expiring", func(interface{}, bool) (interface{}, bool, error) { called = true; return "stale", false, nil }); err != nil || ok || called {
				t.Fatalf("过期租约仍被使用: %t %t %v", ok, called, err)
			}
		})
	}
}
