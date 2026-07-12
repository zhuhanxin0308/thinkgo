package driver

import (
	"errors"
	"fmt"
	"math"
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

func (*nonRawCacheConnection) Select(string, string, []string, []interface{}, string, int, int) ([]map[string]interface{}, error) {
	return nil, nil
}
func (*nonRawCacheConnection) Insert(string, map[string]interface{}) (int64, error) { return 0, nil }
func (*nonRawCacheConnection) Update(string, map[string]interface{}, []string, []interface{}) (int64, error) {
	return 0, nil
}
func (*nonRawCacheConnection) Delete(string, []string, []interface{}) (int64, error) { return 0, nil }
func (*nonRawCacheConnection) Count(string, []string, []interface{}) (int64, error)  { return 0, nil }
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

func (c *failingCacheConnection) Select(string, string, []string, []interface{}, string, int, int) ([]map[string]interface{}, error) {
	return nil, c.err
}
func (c *failingCacheConnection) Insert(string, map[string]interface{}) (int64, error) {
	return 0, c.err
}
func (c *failingCacheConnection) Update(string, map[string]interface{}, []string, []interface{}) (int64, error) {
	return 0, c.err
}
func (c *failingCacheConnection) Delete(string, []string, []interface{}) (int64, error) {
	return 0, c.err
}
func (c *failingCacheConnection) Count(string, []string, []interface{}) (int64, error) {
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
	lock        sync.Mutex
	executedSQL []string
	rows        map[string]map[string]interface{}
	selectHook  func(string)
}

func newRecordingCacheConn() *recordingCacheConn {
	return &recordingCacheConn{rows: make(map[string]map[string]interface{})}
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

func (c *recordingCacheConn) Select(_ string, _ string, _ []string, args []interface{}, _ string, _ int, _ int) ([]map[string]interface{}, error) {
	c.lock.Lock()
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

func (c *recordingCacheConn) Insert(_ string, data map[string]interface{}) (int64, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	key := fmt.Sprint(data["key"])
	if _, exists := c.rows[key]; exists {
		return 0, errors.New("duplicate key")
	}
	c.rows[key] = cloneCacheRow(data)
	return 1, nil
}

func (c *recordingCacheConn) Update(_ string, data map[string]interface{}, _ []string, args []interface{}) (int64, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	key := cacheKeyFromArgs(args)
	if _, exists := c.rows[key]; !exists {
		return 0, nil
	}
	c.rows[key] = cloneCacheRow(data)
	return 1, nil
}

func (c *recordingCacheConn) Delete(_ string, where []string, args []interface{}) (int64, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if len(where) == 0 || len(args) == 0 {
		count := int64(len(c.rows))
		c.rows = make(map[string]map[string]interface{})
		return count, nil
	}
	key := cacheKeyFromArgs(args)
	row, exists := c.rows[key]
	if !exists {
		return 0, nil
	}
	if len(args) > 1 && fmt.Sprint(row["expiry"]) != fmt.Sprint(args[1]) {
		return 0, nil
	}
	delete(c.rows, key)
	return 1, nil
}

func (c *recordingCacheConn) Count(_ string, _ []string, args []interface{}) (int64, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
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
