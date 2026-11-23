package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

func TestDBSubMillisecondExpiryAndLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 在毫秒末尾开始，确保申请的时长跨越刻度，并避开旧秒级时间戳的兼容区间。
		start := time.Date(2024, 1, 1, 0, 0, 0, 950_000, time.UTC)
		time.Sleep(time.Until(start))
		store, err := NewDB(newRecordingCacheConn(), "cache_records")
		if err != nil {
			t.Fatal(err)
		}
		const ttl = 100 * time.Microsecond
		if err := store.Set("item", "value", ttl); err != nil {
			t.Fatal(err)
		}
		if acquired, err := store.AcquireLock("job", "first", ttl); err != nil || !acquired {
			t.Fatalf("首次获锁失败：%t %v", acquired, err)
		}
		time.Sleep(ttl - time.Nanosecond)
		if got, found, err := store.Get("item"); err != nil || !found || got != "value" {
			t.Fatalf("请求时长结束前缓存已过期：%v %t %v", got, found, err)
		}
		if acquired, err := store.AcquireLock("job", "second", time.Second); err != nil || acquired {
			t.Fatalf("原持有者租约尚未到期：%t %v", acquired, err)
		}
		if renewed, err := store.RenewLock("job", "first", ttl); err != nil || !renewed {
			t.Fatalf("亚毫秒租约续期失败：%t %v", renewed, err)
		}
		time.Sleep(ttl - time.Nanosecond)
		if acquired, err := store.AcquireLock("job", "second", time.Second); err != nil || acquired {
			t.Fatalf("续租时长结束前被抢占：%t %v", acquired, err)
		}
		time.Sleep(time.Millisecond)
		if found, err := store.Has("item"); err != nil || found {
			t.Fatalf("毫秒截止后仍命中：%t %v", found, err)
		}
		if acquired, err := store.AcquireLock("job", "second", time.Second); err != nil || !acquired {
			t.Fatalf("截止后无法接管：%t %v", acquired, err)
		}
	})
}

func TestDBNonPositiveExpiry(t *testing.T) {
	store, err := NewDB(newRecordingCacheConn(), "cache_records")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("item", "permanent", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("item", "invalid", -time.Nanosecond); !errors.Is(err, ErrInvalidDriverTTL) {
		t.Fatal(err)
	}
	if got, found, expiry, err := store.getStored("item"); err != nil || !found || got != "permanent" || expiry != 0 {
		t.Fatalf("永久值被改写：%v %t %d %v", got, found, expiry, err)
	}
	for _, ttl := range []time.Duration{0, -time.Nanosecond} {
		if ok, err := store.AcquireLock("job", "owner", ttl); ok || !errors.Is(err, ErrInvalidCacheLock) {
			t.Fatalf("接受了非正租约时长 %v：%t %v", ttl, ok, err)
		}
		if ok, err := store.RenewLock("job", "owner", ttl); ok || !errors.Is(err, ErrInvalidCacheLock) {
			t.Fatalf("接受了非正续租时长 %v：%t %v", ttl, ok, err)
		}
	}
}

func TestDBExpiryKeepsExactMillisecondDeadline(t *testing.T) {
	start := time.Unix(1_760_000_000, 750_000)
	for _, check := range []struct {
		ttl  time.Duration
		want int64
	}{
		{250 * time.Microsecond, start.UnixMilli() + 1},
		{250*time.Microsecond + time.Nanosecond, start.UnixMilli() + 2},
	} {
		if got := dbExpiryMillis(start, check.ttl); got != check.want {
			t.Fatalf("截止刻度错误：时长=%v 得到=%d 期望=%d", check.ttl, got, check.want)
		}
	}
}

func TestDBLogicalKeyLengthAcrossNamespaces(t *testing.T) {
	store, err := NewDB(newRecordingCacheConn(), "cache_records")
	if err != nil {
		t.Fatal(err)
	}
	outer, err := cache.NewNamespaceDriver(store, strings.Repeat("p", 1024), "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := cache.NewNamespaceDriver(outer, "租户:", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{strings.Repeat("a", 256), strings.Repeat("租", 341) + "x"} {
		for name, driver := range map[string]cache.Driver{"直接": store, "单层": outer, "嵌套": inner} {
			if err := driver.Set(key, name, 0); err != nil {
				t.Fatalf("%s 写入 %d 字节失败：%v", name, len(key), err)
			}
		}
		for name, driver := range map[string]cache.Driver{"直接": store, "单层": outer, "嵌套": inner} {
			if got, found, err := driver.Get(key); err != nil || !found || got != name {
				t.Fatalf("%s 的 %d 字节业务键越界或串用：%v %t %v", name, len(key), got, found, err)
			}
			if err := driver.Delete(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	long := strings.Repeat("x", 1025)
	for _, driver := range []cache.Driver{store, outer, inner} {
		if err := driver.Set(long, "value", 0); !errors.Is(err, ErrCacheKeyTooLong) {
			t.Fatalf("超长键未被拒绝：%v", err)
		}
	}
	for _, invalid := range []string{
		cachekey.Namespace(long) + "k",
		cachekey.NamespacePrefix + "v2:00::" + long,
	} {
		if err := store.Set(invalid, "value", 0); !errors.Is(err, ErrCacheKeyTooLong) {
			t.Fatalf("非法命名空间帧绕过了业务键上限：%v", err)
		}
	}
	manager := cache.NewCache(nil, outer)
	key := strings.Repeat("k", 1024)
	if err := manager.Set(key, "public", 0); err != nil {
		t.Fatal(err)
	}
	if got, found, err := manager.Get(key); err != nil || !found || got != "public" {
		t.Fatalf("公开接口未能按原键取回：%v %t %v", got, found, err)
	}
}

// 在首个未命中的 UPDATE 返回后插入另一请求，固定原先漏写的交错顺序。
type afterMissUpdateConnection struct {
	db.Connection
	afterMiss func()
}

func (c *afterMissUpdateConnection) Update(ctx context.Context, request db.UpdateRequest) (db.UpdateResult, error) {
	result, err := c.Connection.Update(ctx, request)
	if err == nil && result.Affected == 0 && c.afterMiss != nil {
		afterMiss := c.afterMiss
		c.afterMiss = nil
		afterMiss()
	}
	return result, err
}

func checkDBUpsertInterleaving(t *testing.T, writerConnection db.Connection, other *DB) {
	t.Helper()
	for _, previous := range []string{"different", "requested"} {
		t.Run(previous, func(t *testing.T) {
			connection := &afterMissUpdateConnection{Connection: writerConnection}
			writer, err := NewDB(connection, other.table)
			if err != nil {
				t.Fatal(err)
			}
			connection.afterMiss = func() {
				if err := other.Set(previous, previous, time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Set(previous, "requested", 0); err != nil {
				t.Fatal(err)
			}
			got, found, expiry, err := other.getStored(previous)
			if connection.afterMiss != nil || err != nil || !found || got != "requested" || expiry != 0 {
				t.Fatalf("并发插入后未保存请求值及有效期：%v %t %d %v", got, found, expiry, err)
			}
		})
	}
}

func TestDBFirstWriteUpsertInterleaving(t *testing.T) {
	connection := newRecordingCacheConn()
	other, err := NewDB(connection, "cache_records")
	if err != nil {
		t.Fatal(err)
	}
	checkDBUpsertInterleaving(t, connection, other)
}

// 只报告变化行的连接在同一毫秒内续租时可能返回零，归属和有效期必须同时成立。
func TestDBRenewUnchangedLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		connection := newRecordingCacheConn()
		connection.reportUnchanged = true
		store, err := NewDB(connection, "cache_records")
		if err != nil {
			t.Fatal(err)
		}
		const ttl = time.Millisecond
		if ok, err := store.AcquireLock("job", "owner", ttl); err != nil || !ok {
			t.Fatalf("首次获锁失败：%t %v", ok, err)
		}
		if ok, err := store.RenewLock("job", "owner", ttl); err != nil || !ok {
			t.Errorf("原持有者续租到相同刻度应成功：%t %v", ok, err)
		}
		if ok, err := store.RenewLock("job", "OWNER", ttl); err != nil || ok {
			t.Errorf("不同持有者不能凭同一到期刻度续租：%t %v", ok, err)
		}
		// 查询已经读到原记录，结果交给驱动前租约到期。确认不能使用 UPDATE 前的旧时刻。
		connection.selectHook = func(string) { time.Sleep(ttl) }
		if ok, err := store.RenewLock("job", "owner", ttl); err != nil || ok {
			t.Errorf("确认时已经过期的租约不能续租成功：%t %v", ok, err)
		}
	})
}
