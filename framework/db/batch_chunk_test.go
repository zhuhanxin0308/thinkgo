package db

import (
	"strings"
	"testing"

	"thinkgo/framework/db/builder"
)

// batchRecorderConn 记录所有执行过的写 SQL，用于断言批量插入分批行为。
type batchRecorderConn struct {
	execSQL  []string
	execArgs [][]interface{}
}

func (c *batchRecorderConn) Select(table, fields string, where []string, args []interface{}, order string, limit, offset int) ([]map[string]interface{}, error) {
	return nil, nil
}
func (c *batchRecorderConn) Insert(string, map[string]interface{}) (int64, error) { return 1, nil }
func (c *batchRecorderConn) Update(string, map[string]interface{}, []string, []interface{}) (int64, error) {
	return 1, nil
}
func (c *batchRecorderConn) Delete(string, []string, []interface{}) (int64, error) { return 1, nil }
func (c *batchRecorderConn) Count(string, []string, []interface{}) (int64, error)  { return 0, nil }
func (c *batchRecorderConn) Close() error                                          { return nil }
func (c *batchRecorderConn) Query(sql string, args ...interface{}) ([]map[string]interface{}, error) {
	return nil, nil
}
func (c *batchRecorderConn) Execute(sql string, args ...interface{}) (int64, error) {
	c.execSQL = append(c.execSQL, sql)
	c.execArgs = append(c.execArgs, args)
	return 1, nil
}

// chunkRecorderConn 按主键游标返回分页数据，用于验证 ChunkById 的 keyset 行为。
type chunkRecorderConn struct {
	selects   []string // 记录每次 Select 的 where 条件拼接
	whereArgs [][]interface{}
	pages     [][]map[string]interface{}
	idx       int
}

func (c *chunkRecorderConn) Select(table, fields string, where []string, args []interface{}, order string, limit, offset int) ([]map[string]interface{}, error) {
	c.selects = append(c.selects, strings.Join(where, " AND "))
	c.whereArgs = append(c.whereArgs, args)
	if c.idx >= len(c.pages) {
		return []map[string]interface{}{}, nil
	}
	page := c.pages[c.idx]
	c.idx++
	return page, nil
}
func (c *chunkRecorderConn) Insert(string, map[string]interface{}) (int64, error) { return 1, nil }
func (c *chunkRecorderConn) Update(string, map[string]interface{}, []string, []interface{}) (int64, error) {
	return 1, nil
}
func (c *chunkRecorderConn) Delete(string, []string, []interface{}) (int64, error) { return 1, nil }
func (c *chunkRecorderConn) Count(string, []string, []interface{}) (int64, error)  { return 0, nil }
func (c *chunkRecorderConn) Close() error                                          { return nil }

// TestInsertAllRequiresSQLConnection 验证批量插入要求 SQL 连接（非 SQL 连接应报错而非静默）。
// 真实分批落库由 connector 包的 SQLite 集成测试覆盖（TestSqliteInsertAllBatching）。
func TestInsertAllRequiresSQLConnection(t *testing.T) {
	database := NewDB(&batchRecorderConn{})
	_, err := database.Table("users").InsertAll([]map[string]interface{}{{"a": 1, "b": 2, "c": 3}})
	if err == nil {
		t.Fatal("非 SQL 连接的批量插入应返回错误")
	}
}

// TestInsertAllSplitsByDialectBindLimit 验证批量插入按不同 SQL 方言的参数上限计算分批大小。
func TestInsertAllSplitsByDialectBindLimit(t *testing.T) {
	cases := []struct {
		name   string
		build  Builder
		fields int
		want   int
	}{
		{name: "mysql", build: &builder.Mysql{}, fields: 3, want: 20000},
		{name: "sqlserver", build: &builder.Sqlsrv{}, fields: 3, want: 700},
		{name: "sqlite", build: &builder.Sqlite{}, fields: 3, want: 333},
		{name: "postgresql", build: &builder.Pgsql{}, fields: 3, want: 20000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := insertAllBatchRows(tc.build, tc.fields); got != tc.want {
				t.Fatalf("%s 每批行数不正确，期望 %d，实际 %d", tc.name, tc.want, got)
			}
		})
	}
}

// TestInsertAllRejectsEmptyRowWithoutPanic 验证空行会被明确拒绝，
// 避免按字段数计算批次大小时触发除零崩溃。
func TestInsertAllRejectsEmptyRowWithoutPanic(t *testing.T) {
	database := NewDB(&SQLConnection{Builder: &builder.Sqlite{}})

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("InsertAll 遇到空行应返回错误而不是 panic: %v", recovered)
		}
	}()

	_, err := database.Table("users").InsertAll([]map[string]interface{}{{}})
	if err == nil {
		t.Fatal("InsertAll 遇到空行应返回错误")
	}
}

// TestChunkByIdUsesKeysetCursor 验证 ChunkById 通过主键游标推进，而非 OFFSET。
func TestChunkByIdUsesKeysetCursor(t *testing.T) {
	conn := &chunkRecorderConn{
		pages: [][]map[string]interface{}{
			{{"id": int64(1)}, {"id": int64(2)}}, // 第 1 批，满 2 条
			{{"id": int64(5)}},                   // 第 2 批，不足 2 条 → 结束
		},
	}
	database := NewDB(conn)

	var seen []interface{}
	err := database.Table("users").ChunkById(2, "id", func(rows []map[string]interface{}) bool {
		for _, r := range rows {
			seen = append(seen, r["id"])
		}
		return true
	})
	if err != nil {
		t.Fatalf("ChunkById 不应报错: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("应遍历 3 条记录，实际 %d", len(seen))
	}
	// 第 1 次查询无游标条件；第 2 次应带 id > 2 的游标条件。
	if len(conn.selects) < 2 {
		t.Fatalf("应至少发起 2 次查询，实际 %d", len(conn.selects))
	}
	if conn.selects[0] != "" {
		t.Fatalf("首批查询不应有游标条件，实际 %q", conn.selects[0])
	}
	if !strings.Contains(conn.selects[1], "id > ?") {
		t.Fatalf("第二批应使用主键游标 id > ?，实际 %q", conn.selects[1])
	}
	if len(conn.whereArgs[1]) != 1 || conn.whereArgs[1][0] != int64(2) {
		t.Fatalf("第二批游标值应为上一批末尾主键 2，实际 %v", conn.whereArgs[1])
	}
}
