//go:build cgo

package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/cache"
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

// 通过公开管理器和同一租约事务验证裸大整数往返，并保留计数器的 int64 限制。
func TestSQLiteWideIntegerRoundTrip(t *testing.T) {
	_, store, _ := newGenerationInterleavingDrivers(t, "")
	manager := cache.NewCache(nil, store)
	positive := json.Number(strings.Repeat("9", 400))
	negative := json.Number("-" + positive.String())
	const key = "wide"
	if err := manager.Set(key, positive, 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetMany(map[string]interface{}{"negative": negative}, 0); err != nil {
		t.Fatal(err)
	}
	row, _, err := store.readStoredRecord(mustStorageKey(t, store, key))
	if err != nil || row == nil {
		t.Fatalf("无法取得数据库原始值：%v", err)
	}
	raw, err := databaseCacheBytes(row["value"])
	if err != nil || string(raw) != positive.String() {
		t.Fatalf("整数未按原 JSON 数字格式写入：%s, %v", raw, err)
	}
	if _, err := store.Inc(key, 1); !errors.Is(err, ErrInvalidCounterValue) {
		t.Fatalf("超界整数递增应失败：%v", err)
	}
	if _, err := store.Dec(key, 1); !errors.Is(err, ErrInvalidCounterValue) {
		t.Fatalf("超界整数递减应失败：%v", err)
	}
	if found, err := store.Has(key); err != nil || !found {
		t.Errorf("驱动未找到已写入的整数：%t, %v", found, err)
	}
	if values, err := manager.GetMany([]string{key, "negative"}); err != nil || values[key] != positive || values["negative"] != negative {
		t.Errorf("批量读取未保留整数：%v, %v", values, err)
	}
	const lock, owner = "lease", "owner"
	if ok, err := store.AcquireLock(lock, owner, time.Minute); err != nil || !ok {
		t.Fatalf("取得租约失败：%t, %v", ok, err)
	}
	ok, err := store.UpdateIfLockOwnerContext(t.Context(), key, 0, lock, owner, func(value interface{}, found bool) (interface{}, bool, error) {
		if !found || value != positive {
			return nil, false, errors.New("租约回调未取得精确的旧整数")
		}
		return negative, false, nil
	})
	if err != nil || !ok {
		t.Fatalf("租约更新失败：%t, %v", ok, err)
	}
	if value, found, err := manager.Get(key); err != nil || !found || value != negative {
		t.Fatalf("获取租约提交结果失败：%v, %t, %v", value, found, err)
	}
}
