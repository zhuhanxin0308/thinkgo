package redis

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestRedisLeaseChangesDuringTransaction 验证回调之后租约被接管时，EXEC 不会提交旧计算结果。
func TestRedisLeaseChangesDuringTransaction(t *testing.T) {
	backend := newConditionalRedis(t)
	const lockKey, key = "lease", "value"
	const ttl = time.Minute
	if ok, err := backend.AcquireLock(lockKey, "old", ttl); err != nil || !ok {
		t.Fatal(err)
	}
	calls := 0
	committed, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, "old", func(interface{}, bool) (interface{}, bool, error) {
		calls++
		if _, err := backend.ReleaseLock(lockKey, "old"); err != nil {
			return nil, false, err
		}
		if acquired, err := backend.AcquireLock(lockKey, "new", ttl); err != nil || !acquired {
			return nil, false, errors.New("新 owner 获取失败")
		}
		if err := backend.Set(key, "successor", ttl); err != nil {
			return nil, false, err
		}
		return "stale", false, nil
	})
	if err != nil || committed || calls != 1 {
		t.Fatalf("丢锁后仍然提交: %t %d %v", committed, calls, err)
	}
	if value, found, err := backend.Get(key); err != nil || !found || value != "successor" {
		t.Fatalf("后继值被覆盖: %v %t %v", value, found, err)
	}
}

// TestRedisExpiredLeaseCannotCommit 验证计算结束时租约刚过期也不能提交，永久值和毫秒 TTL 均保持原状。
func TestRedisExpiredLeaseCannotCommit(t *testing.T) {
	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewRedis(map[string]interface{}{"host": server.Host(), "port": port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	const key, lockKey, owner = "value", "lease", "owner"
	if ok, err := backend.AcquireLock(lockKey, owner, time.Minute); err != nil || !ok {
		t.Fatal(err)
	}
	if err := backend.Set(key, "existing", 0); err != nil {
		t.Fatal(err)
	}
	ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, time.Nanosecond, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) {
		server.FastForward(2 * time.Minute)
		return "stale", false, nil
	})
	if err != nil || ok {
		t.Fatalf("过期租约仍然提交: %t %v", ok, err)
	}
	if value, found, err := backend.Get(key); err != nil || !found || value != "existing" {
		t.Fatalf("旧值被损坏: %v %t %v", value, found, err)
	}
	if ok, err := backend.AcquireLock(lockKey, "new", time.Minute); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, time.Nanosecond, lockKey, "new", func(interface{}, bool) (interface{}, bool, error) { return "fresh", false, nil }); err != nil || !ok {
		t.Fatalf("新 owner 提交失败: %t %v", ok, err)
	}
	if ttl := server.TTL(key); ttl != time.Millisecond {
		t.Fatalf("亚毫秒 TTL 应按后端精度保存: %v", ttl)
	}
}
