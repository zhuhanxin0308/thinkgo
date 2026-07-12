package db

import (
	"errors"
	"math"
	"strings"
	"testing"

	"thinkgo/framework/db/builder"
)

// TestAccumulateAffectedRowsRejectsNegativeAndOverflow 验证批量写入累计数量
// 不会因驱动异常值或 int64 溢出变成错误的成功结果。
func TestAccumulateAffectedRowsRejectsNegativeAndOverflow(t *testing.T) {
	if total, err := accumulateAffectedRows(4, 3); err != nil || total != 7 {
		t.Fatalf("合法影响行数累计错误: total=%d err=%v", total, err)
	}
	for _, values := range [][2]int64{{0, -1}, {math.MaxInt64, 1}} {
		if _, err := accumulateAffectedRows(values[0], values[1]); !errors.Is(err, ErrInvalidAggregateValue) {
			t.Fatalf("非法影响行数 %v 应返回 ErrInvalidAggregateValue，实际为 %v", values, err)
		}
	}
}

// batchRecorderConn 记录所有执行过的写 SQL，用于断言批量插入分批行为。
type batchRecorderConn struct {
	execSQL  []string
	execArgs [][]interface{}
}

// dialectBatchBuilder 模拟需要自定义批量语法的 SQL 方言，
// 用于验证查询层会优先调用方言实现而不是固定拼接多行 VALUES。
type dialectBatchBuilder struct {
	builder.Sqlite
	called int
}

func (b *dialectBatchBuilder) InsertBatch(_ string, fields []string, rows []map[string]interface{}) (string, []interface{}) {
	b.called++
	values := make([]interface{}, 0, len(fields)*len(rows))
	for _, row := range rows {
		for _, field := range fields {
			values = append(values, row[field])
		}
	}
	return "DIALECT INSERT (?, ?), (?, ?)", values
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

// TestInsertAllExecutesParameterizedBatch 验证核心批量写入路径稳定排序字段、绑定全部值，
// 返回驱动报告的影响行数，并且不修改调用方提供的数据。
func TestInsertAllExecutesParameterizedBatch(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	database := NewDB(connection)
	rows := []map[string]interface{}{
		{"name": "alice", "email": "alice@example.com"},
		{"email": "bob@example.com", "name": "bob"},
	}

	affected, err := database.Table("users").InsertAll(rows)
	if err != nil || affected != 2 {
		t.Fatalf("批量写入结果错误: affected=%d err=%v", affected, err)
	}
	wantSQL := `INSERT INTO "users" ("email", "name") VALUES (?, ?), (?, ?)`
	if got := recorder.recordedQuery(); got != wantSQL {
		t.Fatalf("批量写入 SQL 错误: got=%q want=%q", got, wantSQL)
	}
	if rows[0]["name"] != "alice" || rows[1]["email"] != "bob@example.com" {
		t.Fatalf("批量写入不得修改调用方数据: %#v", rows)
	}
}

// TestInsertAllUsesDialectBatchBuilder 验证 Oracle 等特殊方言可以接管批量 SQL 生成，
// 同时仍由统一执行路径负责参数绑定、影响行数和事务处理。
func TestInsertAllUsesDialectBatchBuilder(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	dialect := &dialectBatchBuilder{}
	connection.Builder = dialect
	database := NewDB(connection)

	affected, err := database.Table("users").InsertAll([]map[string]interface{}{
		{"name": "alice", "email": "alice@example.com"},
		{"email": "bob@example.com", "name": "bob"},
	})
	if err != nil || affected != 2 {
		t.Fatalf("方言批量写入结果错误: affected=%d err=%v", affected, err)
	}
	if dialect.called != 1 {
		t.Fatalf("方言批量构建器应调用一次，实际为 %d", dialect.called)
	}
	if got := recorder.recordedQuery(); got != "DIALECT INSERT (?, ?), (?, ?)" {
		t.Fatalf("未使用方言批量 SQL，实际为 %q", got)
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
		{name: "mysql", build: &builder.Mysql{}, fields: 3, want: 21845},
		{name: "sqlserver", build: &builder.Sqlsrv{}, fields: 3, want: 700},
		{name: "sqlite", build: &builder.Sqlite{}, fields: 3, want: 333},
		{name: "postgresql", build: &builder.Pgsql{}, fields: 3, want: 21845},
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

// TestChunkRejectsNilCallback 验证无回调时返回可识别错误而不是触发空指针崩溃。
func TestChunkRejectsNilCallback(t *testing.T) {
	database := NewDB(&chunkRecorderConn{})
	if err := database.Table("users").Chunk(10, nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("Chunk 的 nil 回调应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if err := database.Table("users").ChunkById(10, "id", nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("ChunkById 的 nil 回调应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestChunkByIdCapturesCursorBeforeCallback 验证业务回调修改结果行不会破坏游标推进。
func TestChunkByIdCapturesCursorBeforeCallback(t *testing.T) {
	connection := &chunkRecorderConn{pages: [][]map[string]interface{}{
		{{"id": int64(1)}, {"id": int64(2)}},
		{{"id": int64(3)}},
	}}
	database := NewDB(connection)
	callbackCount := 0
	err := database.Table("users").ChunkById(2, "id", func(rows []map[string]interface{}) bool {
		callbackCount++
		delete(rows[len(rows)-1], "id")
		return true
	})
	if err != nil {
		t.Fatalf("回调修改结果不应破坏游标: %v", err)
	}
	if callbackCount != 2 {
		t.Fatalf("游标应继续到第二批，实际回调次数为 %d", callbackCount)
	}
}

// TestChunkByIdRejectsMissingOrNonProgressingCursor 验证满批数据必须提供可推进主键。
func TestChunkByIdRejectsMissingOrNonProgressingCursor(t *testing.T) {
	cases := []struct {
		name  string
		pages [][]map[string]interface{}
	}{
		{
			name:  "缺失游标",
			pages: [][]map[string]interface{}{{{"id": int64(1)}, {"name": "missing"}}},
		},
		{
			name: "游标不推进",
			pages: [][]map[string]interface{}{
				{{"id": int64(1)}, {"id": int64(2)}},
				{{"id": int64(1)}, {"id": int64(2)}},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := NewDB(&chunkRecorderConn{pages: testCase.pages})
			err := database.Table("users").ChunkById(2, "id", func([]map[string]interface{}) bool { return true })
			if !errors.Is(err, ErrInvalidDatabaseRow) {
				t.Fatalf("非法游标应返回 ErrInvalidDatabaseRow，实际为 %v", err)
			}
		})
	}
}
