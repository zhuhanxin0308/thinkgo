package db

import (
	"context"
	"errors"
	"fmt"
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
	connectionIdentityState
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

func (c *batchRecorderConn) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	return nil, nil
}
func (c *batchRecorderConn) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	return InsertResult{Affected: 1, ID: int64(1), IDKnown: request.WantsID()}, nil
}
func (c *batchRecorderConn) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{Affected: 1}, nil
}
func (c *batchRecorderConn) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{Deleted: 1}, nil
}
func (c *batchRecorderConn) Count(context.Context, CountRequest) (int64, error) { return 0, nil }
func (c *batchRecorderConn) Close() error                                       { return nil }
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
	connectionIdentityState
	selects    []string // 记录每次 Select 的 where 条件拼接
	whereArgs  [][]interface{}
	orders     []string
	limits     []int
	offsets    []int
	countCalls int
	pages      [][]map[string]interface{}
	idx        int
}

func (c *chunkRecorderConn) Select(_ context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return nil, err
	}
	c.selects = append(c.selects, strings.Join(where, " AND "))
	c.whereArgs = append(c.whereArgs, args)
	c.orders = append(c.orders, request.Order())
	c.limits = append(c.limits, request.Limit())
	c.offsets = append(c.offsets, request.Offset())
	if c.idx >= len(c.pages) {
		return []map[string]interface{}{}, nil
	}
	page := c.pages[c.idx]
	c.idx++
	return page, nil
}
func (c *chunkRecorderConn) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	return InsertResult{Affected: 1, ID: int64(1), IDKnown: request.WantsID()}, nil
}
func (c *chunkRecorderConn) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{Affected: 1}, nil
}
func (c *chunkRecorderConn) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{Deleted: 1}, nil
}
func (c *chunkRecorderConn) Count(context.Context, CountRequest) (int64, error) {
	c.countCalls++
	return 0, nil
}
func (c *chunkRecorderConn) Close() error { return nil }

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

// TestInsertAllSplitsByPacketBudgetAndKeepsAtomicity 验证大字段批量写入会在绑定参数之外按包预算拆分，
// 并且因包预算产生多批时仍自动启用事务。
func TestInsertAllSplitsByPacketBudgetAndKeepsAtomicity(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	connection.Builder = builder.NewMysql(400)
	database := NewDB(connection)
	rows := []map[string]interface{}{
		{"id": int64(1), "payload": strings.Repeat("a", 80)},
		{"id": int64(2), "payload": strings.Repeat("b", 80)},
		{"id": int64(3), "payload": strings.Repeat("c", 80)},
	}

	affected, err := database.Table("users").InsertAll(rows)
	if err != nil {
		t.Fatalf("按包预算分批写入失败: %v", err)
	}
	if affected != 4 {
		t.Fatalf("测试驱动两批各返回 2 行，累计影响行数错误: %d", affected)
	}
	if got := recorder.recordedExecCount(); got != 2 {
		t.Fatalf("包预算应拆成两条 INSERT，实际执行 %d 条", got)
	}
	if got := recorder.recordedBeginCount(); got != 1 {
		t.Fatalf("包预算导致多批时应自动开启一次事务，实际 %d 次", got)
	}
}

// TestBuildBatchInsertChunkRejectsOversizedSingleRow 验证单行已经超过预算时不会退化成发送必失败的 SQL。
func TestBuildBatchInsertChunkRejectsOversizedSingleRow(t *testing.T) {
	build := builder.NewMysql(128)
	_, _, _, err := buildBatchInsertChunk(build, "users", []string{"id", "payload"}, []map[string]interface{}{{
		"id": int64(1), "payload": strings.Repeat("x", 512),
	}}, 1)
	if !errors.Is(err, ErrBatchStatementTooLarge) {
		t.Fatalf("超大单行应返回 ErrBatchStatementTooLarge，实际为 %v", err)
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

// TestSeekPageUsesKeysetWithoutCountOrOffset 验证游标分页只读取下一窗口，不执行 COUNT 或 OFFSET。
func TestSeekPageUsesKeysetWithoutCountOrOffset(t *testing.T) {
	conn := &chunkRecorderConn{
		pages: [][]map[string]interface{}{
			{{"id": int64(1)}, {"id": int64(2)}, {"id": int64(3)}},
			{{"id": int64(3)}, {"id": int64(4)}},
		},
	}
	database := NewDB(conn)
	query := database.Table("users")

	first, err := query.SeekPage(2, "id", nil)
	if err != nil {
		t.Fatalf("首个游标分页不应报错: %v", err)
	}
	if len(first.List) != 2 || first.List[0]["id"] != int64(1) || first.List[1]["id"] != int64(2) {
		t.Fatalf("首个游标分页结果错误: %#v", first.List)
	}
	if first.NextCursor != int64(2) || !first.HasMore || first.PageSize != 2 {
		t.Fatalf("首个游标分页元数据错误: %#v", first)
	}

	second, err := query.SeekPage(2, "id", first.NextCursor)
	if err != nil {
		t.Fatalf("后续游标分页不应报错: %v", err)
	}
	if len(second.List) != 2 || second.List[0]["id"] != int64(3) || second.List[1]["id"] != int64(4) {
		t.Fatalf("后续游标分页结果错误: %#v", second.List)
	}
	if second.NextCursor != nil || second.HasMore {
		t.Fatalf("末页游标分页元数据错误: %#v", second)
	}
	if conn.countCalls != 0 {
		t.Fatalf("SeekPage 不应执行 COUNT，实际执行 %d 次", conn.countCalls)
	}
	if len(conn.orders) != 2 || conn.orders[0] != "id" || conn.orders[1] != "id" {
		t.Fatalf("SeekPage 应固定按游标字段升序读取: %#v", conn.orders)
	}
	if len(conn.limits) != 2 || conn.limits[0] != 3 || conn.limits[1] != 3 {
		t.Fatalf("SeekPage 应多读取一行判断是否还有下一页: %#v", conn.limits)
	}
	if len(conn.offsets) != 2 || conn.offsets[0] != 0 || conn.offsets[1] != 0 {
		t.Fatalf("SeekPage 不应使用 OFFSET: %#v", conn.offsets)
	}
	if len(conn.whereArgs) != 2 || len(conn.whereArgs[1]) != 1 || conn.whereArgs[1][0] != int64(2) {
		t.Fatalf("后续页面应使用上一页末游标: %#v", conn.whereArgs)
	}
}

// TestSeekPageValidatesCursorRows 验证游标字段、类型和严格递增约束。
func TestSeekPageValidatesCursorRows(t *testing.T) {
	tests := []struct {
		name  string
		pages [][]map[string]interface{}
		want  error
	}{
		{
			name:  "缺少游标字段",
			pages: [][]map[string]interface{}{{{"name": "missing"}}},
			want:  ErrInvalidDatabaseRow,
		},
		{
			name:  "游标未严格递增",
			pages: [][]map[string]interface{}{{{"id": int64(1)}, {"id": int64(1)}}},
			want:  ErrInvalidDatabaseRow,
		},
		{
			name:  "默认类型不支持文本",
			pages: [][]map[string]interface{}{{{"id": "a"}}},
			want:  ErrUnsupportedCursorKey,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			database := NewDB(&chunkRecorderConn{pages: testCase.pages})
			_, err := database.Table("users").SeekPage(2, "id", nil)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("错误类型不正确: got=%v want=%v", err, testCase.want)
			}
		})
	}

	database := NewDB(&chunkRecorderConn{pages: [][]map[string]interface{}{{{"id": "a"}, {"id": "b"}}}})
	page, err := database.Table("users").SeekPageWithCodec(2, "id", lexicalCursorCodec{}, nil)
	if err != nil || len(page.List) != 2 {
		t.Fatalf("显式文本 codec 应支持游标分页: page=%#v err=%v", page, err)
	}
}

// TestSeekPageRejectsInvalidAfter 验证传入的游标边界会在发起查询前校验。
func TestSeekPageRejectsInvalidAfter(t *testing.T) {
	conn := &chunkRecorderConn{pages: [][]map[string]interface{}{{{"id": int64(1)}}}}
	database := NewDB(conn)
	_, err := database.Table("users").SeekPage(2, "id", "not-an-integer")
	if !errors.Is(err, ErrUnsupportedCursorKey) {
		t.Fatalf("非法 after 应返回 ErrUnsupportedCursorKey，实际为 %v", err)
	}
	if len(conn.selects) != 0 {
		t.Fatal("非法 after 不应发起数据库查询")
	}
}

func TestChunkByIdRejectsTextWithoutCodec(t *testing.T) {
	database := NewDB(&chunkRecorderConn{pages: [][]map[string]interface{}{
		{{"id": "a"}, {"id": "b"}},
	}})
	err := database.Table("users").ChunkById(2, "id", func([]map[string]interface{}) bool { return true })
	if !errors.Is(err, ErrUnsupportedCursorKey) {
		t.Fatalf("text cursor must require an explicit codec: %v", err)
	}
}

type lexicalCursorCodec struct{}

func (lexicalCursorCodec) Compare(previous, next interface{}) (int, error) {
	previousText, previousOK := previous.(string)
	nextText, nextOK := next.(string)
	if !previousOK || !nextOK {
		return 0, fmt.Errorf("%w: lexical cursor changed from %T to %T", ErrInvalidDatabaseRow, previous, next)
	}
	return strings.Compare(previousText, nextText), nil
}

func TestChunkByIdAllowsExplicitTextCodec(t *testing.T) {
	database := NewDB(&chunkRecorderConn{pages: [][]map[string]interface{}{
		{{"id": "a"}, {"id": "b"}},
		{{"id": "c"}},
	}})
	err := database.Table("users").ChunkByIdWithCodec(2, "id", lexicalCursorCodec{}, func([]map[string]interface{}) bool { return true })
	if err != nil {
		t.Fatalf("explicit lexical cursor codec failed: %v", err)
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
