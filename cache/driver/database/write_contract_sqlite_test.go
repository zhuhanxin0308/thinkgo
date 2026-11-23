//go:build cgo

package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestSQLiteFirstWriteUpsertInterleaving(t *testing.T) {
	// 在两个独立的 WAL 连接池间复用相同交错；UPDATE、查询和插入都在真实 SQLite 上执行。
	writer, other, _ := newGenerationInterleavingDrivers(t, "")
	checkDBUpsertInterleaving(t, writer.conn, other)
}

// SQLite 的 IGNORE 触发器使写操作正常返回零行，用真实驱动检查各条提交路径。
func ignoreSQLiteCacheWrite(t *testing.T, store *DB, event, condition string) {
	t.Helper()
	query := fmt.Sprintf("CREATE TRIGGER ignored_cache_write BEFORE %s ON %s %s BEGIN SELECT RAISE(IGNORE); END", event, store.quoteIdentifier(store.table), condition)
	if _, err := store.conn.(*db.SQLConnection).Execute(query); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteIgnoredCacheInsert(t *testing.T) {
	store, reader, _ := newGenerationInterleavingDrivers(t, "")
	ignoreSQLiteCacheWrite(t, store, "INSERT", "")
	if err := store.Set("item", "value", 0); err == nil {
		t.Error("业务记录未插入，Set 却报告成功")
	}
	if _, found, err := reader.Get("item"); err != nil || found {
		t.Fatalf("插入已被忽略，读取状态错误：%t %v", found, err)
	}
	if count, err := store.Inc("counter", 1); err == nil || count != 0 {
		t.Errorf("计数记录未插入，Inc 返回了未持久化的值：%d %v", count, err)
	}
	if ok, err := store.AcquireLock("job", "owner", time.Minute); err == nil || ok {
		t.Errorf("租约未插入，AcquireLock 未报告写入失败：%t %v", ok, err)
	}
	if _, err := db.NewDB(store.conn).Table(store.table).Where("key", dbGenerationKey).Delete(); err != nil {
		t.Fatal(err)
	}
	if generation, err := store.currentGeneration(); err == nil || generation != 0 {
		t.Errorf("共享代号未插入，驱动仍返回了代号：%d %v", generation, err)
	}
}

func TestSQLiteIgnoredLeaseMutation(t *testing.T) {
	for _, test := range []struct {
		name, event, condition string
		existing, remove       bool
	}{
		{name: "插入数据", event: "INSERT"},
		{name: "更新数据", event: "UPDATE", condition: `WHEN NEW.value = '"next"'`, existing: true},
		{name: "删除数据", event: "DELETE", existing: true, remove: true},
		{name: "恢复持有者", event: "UPDATE", condition: `WHEN OLD.value = 'null'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, reader, _ := newGenerationInterleavingDrivers(t, "")
			const key, lock, owner = "item", "job", "owner"
			if ok, err := store.AcquireLock(lock, owner, time.Minute); err != nil || !ok {
				t.Fatalf("首次获锁失败：%t %v", ok, err)
			}
			if test.existing {
				if err := store.Set(key, "original", 0); err != nil {
					t.Fatal(err)
				}
			}
			ignoreSQLiteCacheWrite(t, store, test.event, test.condition)
			ok, err := store.UpdateIfLockOwnerContext(t.Context(), key, 0, lock, owner, func(any, bool) (any, bool, error) {
				return "next", test.remove, nil
			})
			if ok || err == nil {
				t.Errorf("事务丢失写入时应报告失败：%t %v", ok, err)
			}
			if got, found, err := reader.Get(key); err != nil || found != test.existing || found && got != "original" {
				t.Errorf("事务未回滚到原值：%v %t %v", got, found, err)
			}
			if ok, err := reader.RenewLock(lock, owner, time.Hour); err != nil || !ok {
				t.Errorf("事务结束后原租约持有者丢失：%t %v", ok, err)
			}
		})
	}
}

func TestSQLiteIgnoredRenewalRequiresRequestedExpiry(t *testing.T) {
	store, _, _ := newGenerationInterleavingDrivers(t, "")
	if ok, err := store.AcquireLock("job", "owner", time.Minute); err != nil || !ok {
		t.Fatalf("首次获锁失败：%t %v", ok, err)
	}
	ignoreSQLiteCacheWrite(t, store, "UPDATE", "")
	if ok, err := store.RenewLock("job", "owner", time.Hour); err != nil || ok {
		t.Fatalf("只验证当前持有者会误报未写入的到期时间：%t %v", ok, err)
	}
}

func TestSQLiteLeaseAcceptsExistingPostcondition(t *testing.T) {
	store, reader, _ := newGenerationInterleavingDrivers(t, "")
	const key, lock, owner = "item", "job", "owner"
	if ok, err := store.AcquireLock(lock, owner, time.Minute); err != nil || !ok {
		t.Fatalf("首次获锁失败：%t %v", ok, err)
	}
	if err := store.Set(key, "stable", 0); err != nil {
		t.Fatal(err)
	}
	ignoreSQLiteCacheWrite(t, store, "UPDATE", `WHEN NEW.value = '"stable"'`)
	// 相同值和相同有效期已持久化，更新返回零行仍满足回调请求。
	if ok, err := store.UpdateIfLockOwnerContext(t.Context(), key, 0, lock, owner, func(current any, _ bool) (any, bool, error) {
		return current, false, nil
	}); err != nil || !ok {
		t.Fatalf("符合目标状态的幂等更新失败：%t %v", ok, err)
	}
	// 已不存在的记录无需实际删除，零行同样满足回调请求。
	if ok, err := store.UpdateIfLockOwnerContext(t.Context(), "missing", 0, lock, owner, func(any, bool) (any, bool, error) {
		return nil, true, nil
	}); err != nil || !ok {
		t.Fatalf("幂等删除失败：%t %v", ok, err)
	}
	if got, found, err := reader.Get(key); err != nil || !found || got != "stable" {
		t.Fatalf("幂等提交后原值不可读：%v %t %v", got, found, err)
	}
}

func TestSQLiteLeasedWriteRoundsExpiryUp(t *testing.T) {
	writer, reader, _ := newGenerationInterleavingDrivers(t, "")
	const key, lease, owner = "item", "job", "first"
	if acquired, err := writer.AcquireLock(lease, owner, time.Minute); err != nil || !acquired {
		t.Fatalf("首次获锁失败：%t %v", acquired, err)
	}
	physical := mustStorageKey(t, reader, key)
	const ttl = time.Microsecond
	var earliest time.Time
	written, err := writer.UpdateIfLockOwnerContext(t.Context(), key, ttl, lease, owner, func(any, bool) (any, bool, error) {
		earliest = time.Now().Add(ttl)
		return "guarded", false, nil
	})
	if !written || err != nil {
		t.Fatalf("事务提交失败：%t %v", written, err)
	}
	// 与回调返回时的最早合法截止点比较，避免机器调度影响读取业务值的时刻。
	row, err := db.NewDB(reader.conn).Table(reader.table).Where("key", physical).Find()
	if err != nil || row == nil {
		t.Fatalf("无法核对事务提交的记录：%v %v", row, err)
	}
	expiry, err := parseDBExpiry(row["expiry"])
	if err != nil || time.UnixMilli(expiry).Before(earliest) {
		t.Fatalf("事务过早到期：expiry=%d 最早截止=%v 错误=%v", expiry, earliest, err)
	}
}
