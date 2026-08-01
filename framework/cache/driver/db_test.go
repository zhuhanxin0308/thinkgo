package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"
)

// TestDBCacheEnsureTableCreatesSchema 验证 DB 缓存驱动会创建可用缓存表结构。
func TestDBCacheEnsureTableCreatesSchema(t *testing.T) {
	conn := newRecordingCacheConn()
	cache, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	if err = cache.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable 不应失败: %v", err)
	}
	if len(conn.executedSQL) != 1 {
		t.Fatalf("EnsureTable 应执行 1 条建表 SQL，实际 %d", len(conn.executedSQL))
	}
	sql := conn.executedSQL[0]
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS `think_cache`",
		"`key` VARCHAR(255) PRIMARY KEY",
		"`value` TEXT NOT NULL",
		"`expiry` BIGINT NOT NULL DEFAULT 0",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("建表 SQL 缺少片段 %q，实际为 %s", fragment, sql)
		}
	}
}

// TestDBCacheEnsureTableUsesDialectQuoting 验证 DB 缓存建表 SQL 复用数据库方言引用规则。
func TestDBCacheEnsureTableUsesDialectQuoting(t *testing.T) {
	pgCache, err := NewDB(&db.SQLConnection{Builder: &builder.Pgsql{}}, "think_cache")
	if err != nil {
		t.Fatalf("创建 PostgreSQL 缓存驱动失败: %v", err)
	}
	pgSQL := pgCache.createTableSQL()
	for _, fragment := range []string{
		`CREATE TABLE IF NOT EXISTS "think_cache"`,
		`"key" VARCHAR(255) PRIMARY KEY`,
		`"value" TEXT NOT NULL`,
		`"expiry" BIGINT NOT NULL DEFAULT 0`,
	} {
		if !strings.Contains(pgSQL, fragment) {
			t.Fatalf("PostgreSQL 建表 SQL 缺少片段 %q，实际为 %s", fragment, pgSQL)
		}
	}

	sqlsrvCache, err := NewDB(&db.SQLConnection{Builder: &builder.Sqlsrv{}}, "think_cache")
	if err != nil {
		t.Fatalf("创建 SQL Server 缓存驱动失败: %v", err)
	}
	sqlsrvSQL := sqlsrvCache.createTableSQL()
	for _, fragment := range []string{
		`IF OBJECT_ID(N'think_cache', N'U') IS NULL`,
		`CREATE TABLE [think_cache]`,
		`[key] NVARCHAR(255) NOT NULL PRIMARY KEY`,
		`[value] NVARCHAR(MAX) NOT NULL`,
		`[expiry] BIGINT NOT NULL DEFAULT 0`,
	} {
		if !strings.Contains(sqlsrvSQL, fragment) {
			t.Fatalf("SQL Server 建表 SQL 缺少片段 %q，实际为 %s", fragment, sqlsrvSQL)
		}
	}
}

// TestDBCacheRejectsInvalidConstruction 验证连接和表名在执行任何 SQL 前失败。
func TestDBCacheRejectsInvalidConstruction(t *testing.T) {
	if _, err := NewDB(nil, "think_cache"); !errors.Is(err, ErrInvalidCacheDatabase) {
		t.Fatalf("空连接应返回 ErrInvalidCacheDatabase，实际为 %v", err)
	}
	conn := newRecordingCacheConn()
	if _, err := NewDB(conn, "cache;DROP TABLE users"); !errors.Is(err, ErrInvalidCacheTable) {
		t.Fatalf("危险表名应返回 ErrInvalidCacheTable，实际为 %v", err)
	}
	if len(conn.executedSQL) != 0 {
		t.Fatalf("危险表名不应执行 SQL，实际为 %#v", conn.executedSQL)
	}
}

// TestDBCacheResourceIdentity 验证数据库连接与表名共同决定缓存 namespace。
func TestDBCacheResourceIdentity(t *testing.T) {
	conn := newRecordingCacheConn()
	first, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建首个 DB 缓存驱动失败: %v", err)
	}
	second, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建第二个 DB 缓存驱动失败: %v", err)
	}
	otherTable, err := NewDB(conn, "other_cache")
	if err != nil {
		t.Fatalf("创建另一张表的 DB 缓存驱动失败: %v", err)
	}
	if first.CacheResourceIdentity() == "" || first.CacheResourceIdentity() != second.CacheResourceIdentity() {
		t.Fatalf("相同连接和表应产生相同资源标识: first=%q second=%q", first.CacheResourceIdentity(), second.CacheResourceIdentity())
	}
	if first.CacheResourceIdentity() == otherTable.CacheResourceIdentity() {
		t.Fatal("不同缓存表不得共享资源标识")
	}
}

type unsupportedJSONValue struct{}

func (unsupportedJSONValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot encode")
}

// TestDBCacheRoundTripExpiryAndCounter 验证 DB 缓存支持 nil 命中、毫秒 TTL、严格计数和过期时间继承。
func TestDBCacheRoundTripExpiryAndCounter(t *testing.T) {
	conn := newRecordingCacheConn()
	cache, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	if err = cache.Set("nil", nil, 0); err != nil {
		t.Fatalf("写入 nil 缓存失败: %v", err)
	}
	value, found, err := cache.Get("nil")
	if err != nil || !found || value != nil {
		t.Fatalf("DB nil 命中错误: value=%#v found=%t err=%v", value, found, err)
	}
	if err = cache.Set("counter", int64(5), 80*time.Millisecond); err != nil {
		t.Fatalf("写入计数缓存失败: %v", err)
	}
	if value, err := cache.Inc("counter", 2); err != nil || value != 7 {
		t.Fatalf("DB 计数递增失败: value=%d err=%v", value, err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, found, err = cache.Get("counter"); err != nil || found {
		t.Fatalf("DB 计数应继承原 TTL: found=%t err=%v", found, err)
	}
	if err = cache.Set("fraction", 1.5, 0); err != nil {
		t.Fatalf("写入小数失败: %v", err)
	}
	if _, err = cache.Inc("fraction", 1); !errors.Is(err, ErrInvalidCounterValue) {
		t.Fatalf("小数计数值应被拒绝，实际为 %v", err)
	}
	if err = cache.Set("overflow", int64(math.MaxInt64), 0); err != nil {
		t.Fatalf("写入溢出值失败: %v", err)
	}
	if _, err = cache.Inc("overflow", 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("DB 计数溢出应被拒绝，实际为 %v", err)
	}
	if err = cache.Set("unsupported", unsupportedJSONValue{}, 0); err == nil {
		t.Fatal("序列化失败不得写入 DB 缓存")
	}
}

// TestDBCacheConcurrentSetUsesRaceSafeUpsert 验证并发首次写入不会因 Has+Insert 竞态丢失全部更新。
func TestDBCacheConcurrentSetUsesRaceSafeUpsert(t *testing.T) {
	conn := newRecordingCacheConn()
	cache, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	const workers = 32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(value int) {
			defer wait.Done()
			errorsChannel <- cache.Set("shared", value, 0)
		}(index)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("并发 upsert 失败: %v", err)
		}
	}
	if _, found, err := cache.Get("shared"); err != nil || !found {
		t.Fatalf("并发写入后缓存不存在: found=%t err=%v", found, err)
	}
}

// TestDBCacheConcurrentCounterUsesTableLock 验证多个缓存驱动实例并发计数不会丢失更新。
func TestDBCacheConcurrentCounterUsesTableLock(t *testing.T) {
	conn := newRecordingCacheConn()
	first, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建首个 DB 缓存驱动失败: %v", err)
	}
	second, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建第二个 DB 缓存驱动失败: %v", err)
	}
	const workers = 32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			driver := first
			if index%2 == 1 {
				driver = second
			}
			_, incErr := driver.Inc("counter", 1)
			errorsChannel <- incErr
		}(index)
	}
	wait.Wait()
	close(errorsChannel)
	for incErr := range errorsChannel {
		if incErr != nil {
			t.Fatalf("并发 DB 计数失败: %v", incErr)
		}
	}
	if value, found, getErr := first.Get("counter"); getErr != nil || !found || value != float64(workers) {
		t.Fatalf("并发 DB 计数结果错误: value=%#v found=%t err=%v", value, found, getErr)
	}
}

// TestDBCacheUpsertTreatsMatchedUnchangedRowAsSuccess 验证数据库将未变化更新报告为 0 时不会误报插入失败。
func TestDBCacheUpsertTreatsMatchedUnchangedRowAsSuccess(t *testing.T) {
	conn := newRecordingCacheConn()
	driver, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	if err = driver.Set("stable", "same", 0); err != nil {
		t.Fatalf("首次写入缓存失败: %v", err)
	}
	conn.lock.Lock()
	conn.reportUnchanged = true
	conn.lock.Unlock()
	if err = driver.Set("stable", "same", 0); err != nil {
		t.Fatalf("数据库将匹配但未变化报告为 0 时不应失败: %v", err)
	}
	if value, found, getErr := driver.Get("stable"); getErr != nil || !found || value != "same" {
		t.Fatalf("匹配但未变化的缓存未保持可读: value=%#v found=%t err=%v", value, found, getErr)
	}
}

// TestDBCacheLockLeaseAndClearPreservesLock 验证数据库锁的 owner 条件、续租和清空保留语义。
func TestDBCacheLockLeaseAndClearPreservesLock(t *testing.T) {
	conn := newRecordingCacheConn()
	driver, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	if acquired, lockErr := driver.AcquireLock("job", "owner-a", time.Minute); lockErr != nil || !acquired {
		t.Fatalf("数据库锁首次获取失败: acquired=%t err=%v", acquired, lockErr)
	}
	if acquired, lockErr := driver.AcquireLock("job", "owner-b", time.Minute); lockErr != nil || acquired {
		t.Fatalf("活动数据库锁不应被其他 owner 抢占: acquired=%t err=%v", acquired, lockErr)
	}
	if renewed, lockErr := driver.RenewLock("job", "owner-b", time.Minute); lockErr != nil || renewed {
		t.Fatalf("错误 owner 不应续租数据库锁: renewed=%t err=%v", renewed, lockErr)
	}
	if renewed, lockErr := driver.RenewLock("job", "owner-a", time.Minute); lockErr != nil || !renewed {
		t.Fatalf("正确 owner 续租数据库锁失败: renewed=%t err=%v", renewed, lockErr)
	}
	if err = driver.Set("business", "value", 0); err != nil {
		t.Fatalf("写入业务缓存失败: %v", err)
	}
	if err = driver.Clear(); err != nil {
		t.Fatalf("清空数据库缓存失败: %v", err)
	}
	if exists, hasErr := driver.Has("business"); hasErr != nil || exists {
		t.Fatalf("Clear 后业务缓存仍存在: exists=%t err=%v", exists, hasErr)
	}
	if acquired, lockErr := driver.AcquireLock("job", "owner-b", time.Minute); lockErr != nil || acquired {
		t.Fatalf("Clear 不得释放活动数据库锁: acquired=%t err=%v", acquired, lockErr)
	}
	if released, lockErr := driver.ReleaseLock("job", "owner-b"); lockErr != nil || released {
		t.Fatalf("错误 owner 不应释放数据库锁: released=%t err=%v", released, lockErr)
	}
	if released, lockErr := driver.ReleaseLock("job", "owner-a"); lockErr != nil || !released {
		t.Fatalf("正确 owner 释放数据库锁失败: released=%t err=%v", released, lockErr)
	}
}

// TestDBCacheCRUDClearAndCounterDirection 验证 DB 驱动存在判断、删除、清空和计数方向边界。
func TestDBCacheCRUDClearAndCounterDirection(t *testing.T) {
	conn := newRecordingCacheConn()
	driver, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	for _, key := range []string{"first", "second"} {
		if err = driver.Set(key, "value", 0); err != nil {
			t.Fatalf("写入 %q 失败: %v", key, err)
		}
	}
	if exists, hasErr := driver.Has("first"); hasErr != nil || !exists {
		t.Fatalf("Has 未识别 DB 缓存: exists=%t err=%v", exists, hasErr)
	}
	if err = driver.Delete("first"); err != nil {
		t.Fatalf("删除 DB 缓存失败: %v", err)
	}
	if exists, hasErr := driver.Has("first"); hasErr != nil || exists {
		t.Fatalf("删除后 Has 仍命中: exists=%t err=%v", exists, hasErr)
	}
	if value, changeErr := driver.Dec("missing", 2); changeErr != nil || value != -2 {
		t.Fatalf("缺失 DB 计数递减应从 0 开始: value=%d err=%v", value, changeErr)
	}
	if _, changeErr := driver.Inc("negative-step", -1); !errors.Is(changeErr, ErrInvalidCounterStep) {
		t.Fatalf("负递增步长应返回 ErrInvalidCounterStep，实际为 %v", changeErr)
	}
	if _, changeErr := driver.Dec("negative-step", -1); !errors.Is(changeErr, ErrInvalidCounterStep) {
		t.Fatalf("负递减步长应返回 ErrInvalidCounterStep，实际为 %v", changeErr)
	}
	if err = driver.Clear(); err != nil {
		t.Fatalf("清空 DB 缓存失败: %v", err)
	}
	if exists, hasErr := driver.Has("second"); hasErr != nil || exists {
		t.Fatalf("Clear 后仍存在 DB 缓存: exists=%t err=%v", exists, hasErr)
	}
}

// TestDBCacheBackendDataValidation 验证键长度、后端字段类型、旧秒级过期值和解析异常均明确失败。
func TestDBCacheBackendDataValidation(t *testing.T) {
	conn := newRecordingCacheConn()
	driver, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	if err = driver.Set(strings.Repeat("k", maxDBCacheKeyBytes+1), "value", 0); !errors.Is(err, ErrCacheKeyTooLong) {
		t.Fatalf("超长 DB 缓存键应返回 ErrCacheKeyTooLong，实际为 %v", err)
	}
	conn.rows["invalid-value"] = map[string]interface{}{
		"key": "invalid-value", "value": 123, "expiry": int64(0),
	}
	if _, _, err = driver.Get("invalid-value"); !errors.Is(err, ErrCorruptCacheEntry) {
		t.Fatalf("非法 value 字段类型应返回 ErrCorruptCacheEntry，实际为 %v", err)
	}
	conn.rows["invalid-expiry"] = map[string]interface{}{
		"key": "invalid-expiry", "value": `"value"`, "expiry": "invalid",
	}
	if _, _, err = driver.Get("invalid-expiry"); !errors.Is(err, ErrCorruptCacheEntry) {
		t.Fatalf("非法 expiry 字段应返回 ErrCorruptCacheEntry，实际为 %v", err)
	}
	conn.rows["negative-expiry"] = map[string]interface{}{
		"key": "negative-expiry", "value": `"value"`, "expiry": int64(-1),
	}
	if _, _, err = driver.Get("negative-expiry"); !errors.Is(err, ErrCorruptCacheEntry) {
		t.Fatalf("负 expiry 应返回 ErrCorruptCacheEntry，实际为 %v", err)
	}
	conn.rows["legacy-expired"] = map[string]interface{}{
		"key": "legacy-expired", "value": []byte(`"value"`), "expiry": time.Now().Add(-time.Second).Unix(),
	}
	if _, found, getErr := driver.Get("legacy-expired"); getErr != nil || found {
		t.Fatalf("旧秒级过期值应兼容并删除: found=%t err=%v", found, getErr)
	}
	if _, exists := conn.rows["legacy-expired"]; exists {
		t.Fatal("旧秒级过期值读取后未删除")
	}
}

// TestDBExpiredReadDoesNotDeleteConcurrentRefresh 验证过期清理按旧 expiry 条件删除，不会移除并发刷新值。
func TestDBExpiredReadDoesNotDeleteConcurrentRefresh(t *testing.T) {
	conn := newRecordingCacheConn()
	driver, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatalf("创建 DB 缓存驱动失败: %v", err)
	}
	conn.rows["race"] = map[string]interface{}{
		"key": "race", "value": `"expired"`, "expiry": time.Now().Add(-time.Second).UnixMilli(),
	}
	conn.selectHook = func(key string) {
		if key != "race" {
			return
		}
		conn.lock.Lock()
		conn.selectHook = nil
		conn.lock.Unlock()
		if setErr := driver.Set("race", "fresh", 0); setErr != nil {
			t.Errorf("并发刷新缓存失败: %v", setErr)
		}
	}
	if _, found, getErr := driver.Get("race"); getErr != nil || found {
		t.Fatalf("读取到旧过期值: found=%t err=%v", found, getErr)
	}
	if value, found, getErr := driver.Get("race"); getErr != nil || !found || value != "fresh" {
		t.Fatalf("过期清理误删并发刷新值: value=%#v found=%t err=%v", value, found, getErr)
	}
}

type nonRawCacheConnection struct{}

var nonRawCacheConnectionID = db.NewConnectionID("cache-non-raw-test")

func (*nonRawCacheConnection) ConnectionID() db.ConnectionID { return nonRawCacheConnectionID }
func (*nonRawCacheConnection) Select(context.Context, db.SelectRequest) ([]map[string]interface{}, error) {
	return nil, nil
}
func (*nonRawCacheConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) {
	return db.InsertResult{}, nil
}
func (*nonRawCacheConnection) Update(context.Context, db.UpdateRequest) (db.UpdateResult, error) {
	return db.UpdateResult{}, nil
}
func (*nonRawCacheConnection) Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error) {
	return db.DeleteResult{}, nil
}
func (*nonRawCacheConnection) Count(context.Context, db.CountRequest) (int64, error) { return 0, nil }
func (*nonRawCacheConnection) Close() error                                          { return nil }

// TestDBCacheEnsureTableRequiresRawConnection 验证不支持原生 SQL 的连接不会伪造建表成功。
func TestDBCacheEnsureTableRequiresRawConnection(t *testing.T) {
	driver, err := NewDB(&nonRawCacheConnection{}, "think_cache")
	if err != nil {
		t.Fatalf("创建非原生连接缓存驱动失败: %v", err)
	}
	if err = driver.EnsureTable(); !errors.Is(err, ErrInvalidCacheDatabase) {
		t.Fatalf("不支持原生 SQL 应返回 ErrInvalidCacheDatabase，实际为 %v", err)
	}
	var nilDriver *DB
	if err = nilDriver.EnsureTable(); !errors.Is(err, ErrInvalidCacheDatabase) {
		t.Fatalf("nil DB 驱动应返回 ErrInvalidCacheDatabase，实际为 %v", err)
	}
}

type failingCacheConnection struct {
	err error
}

var failingCacheConnectionID = db.NewConnectionID("cache-failing-test")

func (*failingCacheConnection) ConnectionID() db.ConnectionID { return failingCacheConnectionID }
func (c *failingCacheConnection) Select(context.Context, db.SelectRequest) ([]map[string]interface{}, error) {
	return nil, c.err
}
func (c *failingCacheConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) {
	return db.InsertResult{}, c.err
}
func (c *failingCacheConnection) Update(context.Context, db.UpdateRequest) (db.UpdateResult, error) {
	return db.UpdateResult{}, c.err
}
func (c *failingCacheConnection) Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error) {
	return db.DeleteResult{}, c.err
}
func (c *failingCacheConnection) Count(context.Context, db.CountRequest) (int64, error) {
	return 0, c.err
}
func (c *failingCacheConnection) Close() error { return c.err }
func (c *failingCacheConnection) Query(string, ...interface{}) ([]map[string]interface{}, error) {
	return nil, c.err
}
func (c *failingCacheConnection) Execute(string, ...interface{}) (int64, error) {
	return 0, c.err
}

// TestDBCachePropagatesConnectionErrors 验证所有 SQL 读写和建表错误均不被转换成未命中或成功。
func TestDBCachePropagatesConnectionErrors(t *testing.T) {
	backendErr := errors.New("database unavailable")
	driver, err := NewDB(&failingCacheConnection{err: backendErr}, "think_cache")
	if err != nil {
		t.Fatalf("创建失败连接缓存驱动错误: %v", err)
	}
	if _, _, err = driver.Get("key"); !errors.Is(err, backendErr) {
		t.Fatalf("Get 未传播连接错误: %v", err)
	}
	if err = driver.Set("key", "value", 0); !errors.Is(err, backendErr) {
		t.Fatalf("Set 未传播连接错误: %v", err)
	}
	if err = driver.Delete("key"); !errors.Is(err, backendErr) {
		t.Fatalf("Delete 未传播连接错误: %v", err)
	}
	if err = driver.Clear(); !errors.Is(err, backendErr) {
		t.Fatalf("Clear 未传播连接错误: %v", err)
	}
	if _, err = driver.Inc("key", 1); !errors.Is(err, backendErr) {
		t.Fatalf("Inc 未传播连接错误: %v", err)
	}
	if err = driver.EnsureTable(); !errors.Is(err, backendErr) {
		t.Fatalf("EnsureTable 未传播连接错误: %v", err)
	}
}

type recordingCacheConn struct {
	identity        db.ConnectionID
	lock            sync.Mutex
	executedSQL     []string
	rows            map[string]map[string]interface{}
	selectHook      func(string)
	reportUnchanged bool
}

func newRecordingCacheConn() *recordingCacheConn {
	return &recordingCacheConn{
		identity: db.NewConnectionID("cache-recording-test"),
		rows:     make(map[string]map[string]interface{}),
	}
}

func cacheKeyFromArgs(args []interface{}) string {
	if len(args) == 0 {
		return ""
	}
	return fmt.Sprint(args[0])
}

func cloneCacheRow(row map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(row))
	for key, value := range row {
		cloned[key] = value
	}
	return cloned
}

func cachePredicateArgs(predicate db.Predicate) []interface{} {
	var args []interface{}
	for _, clause := range predicate.Clauses() {
		args = append(args, clause.Args...)
	}
	return args
}

func (c *recordingCacheConn) ConnectionID() db.ConnectionID { return c.identity }

func (c *recordingCacheConn) Select(_ context.Context, request db.SelectRequest) ([]map[string]interface{}, error) {
	c.lock.Lock()
	args := cachePredicateArgs(request.Predicate())
	key := cacheKeyFromArgs(args)
	row, exists := c.rows[key]
	hook := c.selectHook
	if !exists {
		c.lock.Unlock()
		return nil, nil
	}
	cloned := cloneCacheRow(row)
	c.lock.Unlock()
	if hook != nil {
		hook(key)
	}
	return []map[string]interface{}{cloned}, nil
}

func (c *recordingCacheConn) Insert(_ context.Context, request db.InsertRequest) (db.InsertResult, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	data := request.Data()
	key := fmt.Sprint(data["key"])
	if _, exists := c.rows[key]; exists {
		return db.InsertResult{}, errors.New("duplicate key")
	}
	c.rows[key] = cloneCacheRow(data)
	return db.InsertResult{Affected: 1, Data: cloneCacheRow(data)}, nil
}

func (c *recordingCacheConn) Update(_ context.Context, request db.UpdateRequest) (db.UpdateResult, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	args := cachePredicateArgs(request.Predicate())
	key := cacheKeyFromArgs(args)
	row, exists := c.rows[key]
	if !exists || !cachePredicateMatches(row, request.Predicate()) {
		return db.UpdateResult{}, nil
	}
	updatedRow := cloneCacheRow(row)
	for field, value := range request.Data() {
		updatedRow[field] = value
	}
	c.rows[key] = updatedRow
	if c.reportUnchanged {
		return db.UpdateResult{Data: cloneCacheRow(updatedRow), ModifiedKnown: true}, nil
	}
	return db.UpdateResult{Affected: 1, Modified: 1, ModifiedKnown: true, Data: cloneCacheRow(updatedRow)}, nil
}

func (c *recordingCacheConn) Delete(_ context.Context, request db.DeleteRequest) (db.DeleteResult, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	clauses := request.Predicate().Clauses()
	if len(clauses) == 0 {
		count := int64(len(c.rows))
		c.rows = make(map[string]map[string]interface{})
		return db.DeleteResult{Deleted: count}, nil
	}
	var deleted int64
	for key, row := range c.rows {
		if cachePredicateMatches(row, request.Predicate()) {
			delete(c.rows, key)
			deleted++
		}
	}
	return db.DeleteResult{Deleted: deleted}, nil
}

func (c *recordingCacheConn) Count(_ context.Context, request db.CountRequest) (int64, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	args := cachePredicateArgs(request.Predicate())
	if _, exists := c.rows[cacheKeyFromArgs(args)]; exists {
		return 1, nil
	}
	return 0, nil
}

func (c *recordingCacheConn) Close() error { return nil }

func (c *recordingCacheConn) Query(string, ...interface{}) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *recordingCacheConn) Execute(sql string, _ ...interface{}) (int64, error) {
	c.lock.Lock()
	c.executedSQL = append(c.executedSQL, sql)
	c.lock.Unlock()
	return 0, nil
}

func cachePredicateMatches(row map[string]interface{}, predicate db.Predicate) bool {
	for _, clause := range predicate.Clauses() {
		argumentIndex := 0
		parts := strings.Fields(strings.ToLower(clause.SQL))
		if len(parts) >= 4 && parts[1] == "not" && parts[2] == "like" {
			if argumentIndex >= len(clause.Args) {
				return false
			}
			pattern := strings.TrimSuffix(fmt.Sprint(clause.Args[argumentIndex]), "%")
			argumentIndex++
			field := strings.Trim(parts[0], "`\"[]")
			if strings.HasPrefix(fmt.Sprint(row[field]), pattern) {
				return false
			}
			continue
		}
		if len(parts) < 3 || argumentIndex >= len(clause.Args) {
			return false
		}
		field := strings.Trim(parts[0], "`\"[]")
		operator := parts[1]
		actual := fmt.Sprint(row[field])
		expected := fmt.Sprint(clause.Args[argumentIndex])
		argumentIndex++
		switch operator {
		case "=":
			if actual != expected {
				return false
			}
		case "<=", ">", ">=", "<":
			actualNumber, actualErr := strconv.ParseInt(actual, 10, 64)
			expectedNumber, expectedErr := strconv.ParseInt(expected, 10, 64)
			if actualErr != nil || expectedErr != nil {
				return false
			}
			switch operator {
			case "<=":
				if actualNumber > expectedNumber {
					return false
				}
			case ">":
				if actualNumber <= expectedNumber {
					return false
				}
			case ">=":
				if actualNumber < expectedNumber {
					return false
				}
			case "<":
				if actualNumber >= expectedNumber {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}
