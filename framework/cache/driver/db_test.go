package driver

import (
	"strings"
	"testing"

	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"
)

// TestDBCacheEnsureTableCreatesSchema 验证 DB 缓存驱动会创建可用缓存表结构。
func TestDBCacheEnsureTableCreatesSchema(t *testing.T) {
	conn := &recordingCacheConn{}
	cache := NewDB(conn, "think_cache")

	if err := cache.EnsureTable(); err != nil {
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
	pgCache := NewDB(&db.SQLConnection{Builder: &builder.Pgsql{}}, "think_cache")
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

	sqlsrvCache := NewDB(&db.SQLConnection{Builder: &builder.Sqlsrv{}}, "think_cache")
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

// TestDBCacheEnsureTableRejectsUnsafeTableName 验证缓存表名必须是安全标识符。
func TestDBCacheEnsureTableRejectsUnsafeTableName(t *testing.T) {
	conn := &recordingCacheConn{}
	cache := NewDB(conn, "cache;DROP TABLE users")

	if err := cache.EnsureTable(); err == nil {
		t.Fatal("危险缓存表名应被拒绝")
	}
	if len(conn.executedSQL) != 0 {
		t.Fatalf("危险表名不应执行 SQL，实际为 %#v", conn.executedSQL)
	}
}

type recordingCacheConn struct {
	executedSQL []string
}

func (c *recordingCacheConn) Select(table, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *recordingCacheConn) Insert(table string, data map[string]interface{}) (int64, error) {
	return 1, nil
}

func (c *recordingCacheConn) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 1, nil
}

func (c *recordingCacheConn) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 1, nil
}

func (c *recordingCacheConn) Count(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *recordingCacheConn) Close() error {
	return nil
}

func (c *recordingCacheConn) Query(sql string, args ...interface{}) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *recordingCacheConn) Execute(sql string, args ...interface{}) (int64, error) {
	c.executedSQL = append(c.executedSQL, sql)
	return 0, nil
}
