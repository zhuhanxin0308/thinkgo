//go:build cgo

package database

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// TestSQLiteLeaseCommitAndRollback 使用真实 SQL 事务验证提交、回滚、owner 恢复及丢锁拒绝。
func TestSQLiteLeaseCommitAndRollback(t *testing.T) {
	pool, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	connection := db.NewSQLConnection(pool, &builder.Sqlite{})
	t.Cleanup(func() { _ = connection.Close() })
	backend, err := NewDB(connection, "cache_records")
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.EnsureTable(); err != nil {
		t.Fatal(err)
	}
	const key, lockKey, owner = "value", "lease", "owner"
	const ttl = time.Minute
	if ok, err := backend.AcquireLock(lockKey, owner, ttl); err != nil || !ok {
		t.Fatal(err)
	}
	write := func(interface{}, bool) (interface{}, bool, error) { return "committed", false, nil }
	if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, write); err != nil || !ok {
		t.Fatalf("写入失败: %v", err)
	}
	wantErr := errors.New("回调失败")
	if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { return "bad", false, wantErr }); ok || !errors.Is(err, wantErr) {
		t.Fatalf("回滚错误: %t %v", ok, err)
	}
	if value, found, err := backend.Get(key); err != nil || !found || value != "committed" {
		t.Fatalf("回滚损坏旧值: %v %t %v", value, found, err)
	}
	if ok, err := backend.RenewLock(lockKey, owner, ttl); err != nil || !ok {
		t.Fatalf("事务未恢复 owner: %v", err)
	}
	if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, write); err != nil || !ok {
		t.Fatalf("覆盖已有值失败: %v", err)
	}
	if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); err != nil || !ok {
		t.Fatalf("删除失败: %v", err)
	}
	if _, found, err := backend.Get(key); err != nil || found {
		t.Fatalf("删除未生效: %t %v", found, err)
	}
	if ok, err := backend.ReleaseLock(lockKey, owner); err != nil || !ok {
		t.Fatalf("释放原 owner 失败: %v", err)
	}
	called := false
	if ok, err := backend.UpdateIfLockOwnerContext(t.Context(), key, ttl, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { called = true; return "stale", false, nil }); err != nil || ok || called {
		t.Fatalf("已丢失的租约仍可提交: %t %t %v", ok, called, err)
	}
}
