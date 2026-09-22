package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// queryHardeningConnection 记录查询层传给驱动的数据，验证框架不会污染调用方输入。
type queryHardeningConnection struct {
	connectionIdentityState
	selectRows  []map[string]interface{}
	insertData  map[string]interface{}
	updateData  map[string]interface{}
	updateWhere []string
	updateArg   []interface{}
	executeSQL  string
	executeArg  []interface{}
}

func (c *queryHardeningConnection) QueryContext(_ context.Context, sqlText string, args ...interface{}) ([]map[string]interface{}, error) {
	return c.Query(sqlText, args...)
}

func (c *queryHardeningConnection) ExecuteContext(_ context.Context, sqlText string, args ...interface{}) (int64, error) {
	return c.Execute(sqlText, args...)
}

func TestSQLServerLockHintIsAttachedToTable(t *testing.T) {
	sqlServer := &builder.Sqlsrv{}
	database := NewDB(&SQLConnection{Builder: sqlServer})
	query, _, err := database.Table("users").WhereField("id", "=", 7).Lock().BuildSelectSQL()
	if err != nil {
		t.Fatalf("build SQL Server locked query: %v", err)
	}
	query = sqlServer.Rebind(query)
	if !strings.Contains(query, "FROM [users] WITH (UPDLOCK, ROWLOCK) WHERE [id] = @p1") {
		t.Fatalf("SQL Server lock hint is in the wrong position: %s", query)
	}
	if strings.HasSuffix(query, "WITH (UPDLOCK, ROWLOCK)") {
		t.Fatalf("SQL Server lock hint must not be emitted as a tail clause: %s", query)
	}
}

// TestAggregateParsesDriverNumericRepresentations 验证聚合值兼容各 SQL 驱动常见数字表示，
// 同时拒绝 NaN、无穷值和非法文本。
func TestAggregateParsesDriverNumericRepresentations(t *testing.T) {
	connection := &queryHardeningConnection{}
	database := NewDB(connection)
	valid := []interface{}{
		float64(7.5), float32(7.5), int64(7), int(7), int8(7), int16(7), int32(7),
		uint(7), uint8(7), uint16(7), uint32(7), uint64(7), []byte(" 7.5 "), "7.5", json.Number("7.5"), nil,
	}
	for _, value := range valid {
		connection.selectRows = []map[string]interface{}{{"tp_aggregate": value}}
		parsed, err := database.Table("users").Avg("score")
		if err != nil {
			t.Fatalf("聚合类型 %T 解析失败: %v", value, err)
		}
		if value == nil && parsed != 0 {
			t.Fatalf("nil 聚合应返回零，实际为 %v", parsed)
		}
	}
	connection.selectRows = nil
	if value, err := database.Table("users").Max("score"); err != nil || value != 0 {
		t.Fatalf("空聚合结果应返回零: value=%v err=%v", value, err)
	}
	for _, value := range []interface{}{math.NaN(), math.Inf(1), float32(math.Inf(-1)), "invalid", []byte("NaN"), struct{}{}} {
		connection.selectRows = []map[string]interface{}{{"tp_aggregate": value}}
		if _, err := database.Table("users").Min("score"); !errors.Is(err, ErrInvalidAggregateValue) {
			t.Fatalf("非法聚合 %T(%v) 应返回 ErrInvalidAggregateValue，实际为 %v", value, value, err)
		}
	}
}

// TestValueAndColumnSupportQualifiedFields 验证限定字段生成 SQL 后，
// 能按驱动返回的末段列名读取单值、列表和键值映射。
func TestValueAndColumnSupportQualifiedFields(t *testing.T) {
	connection := &queryHardeningConnection{selectRows: []map[string]interface{}{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
	}}
	database := NewDB(connection)

	value, err := database.Table("users").Value("users.name")
	if err != nil || value != "alice" {
		t.Fatalf("限定字段 Value 结果错误: value=%#v err=%v", value, err)
	}
	column, err := database.Table("users").Column("users.name")
	if err != nil || !reflect.DeepEqual(column, []interface{}{"alice", "bob"}) {
		t.Fatalf("限定字段 Column 列表错误: value=%#v err=%v", column, err)
	}
	keyed, err := database.Table("users").Column("users.name", "users.id")
	if err != nil || !reflect.DeepEqual(keyed, map[string]interface{}{"1": "alice", "2": "bob"}) {
		t.Fatalf("限定字段 Column 映射错误: value=%#v err=%v", keyed, err)
	}
}

func (c *queryHardeningConnection) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	return c.selectRows, nil
}

func (c *queryHardeningConnection) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	c.insertData = cloneHardeningMap(request.Data())
	return InsertResult{Affected: 1, ID: int64(1), IDKnown: request.WantsID(), Data: request.Data()}, nil
}

func (c *queryHardeningConnection) Update(_ context.Context, request UpdateRequest) (UpdateResult, error) {
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return UpdateResult{}, err
	}
	c.updateData = cloneHardeningMap(request.Data())
	c.updateWhere = append([]string(nil), where...)
	c.updateArg = append([]interface{}(nil), args...)
	return UpdateResult{Affected: 1, Data: request.Data()}, nil
}

func (c *queryHardeningConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{Deleted: 1}, nil
}

func (c *queryHardeningConnection) Count(context.Context, CountRequest) (int64, error) {
	return 0, nil
}

func (c *queryHardeningConnection) Close() error { return nil }

func (c *queryHardeningConnection) Query(string, ...interface{}) ([]map[string]interface{}, error) {
	return c.selectRows, nil
}

func (c *queryHardeningConnection) Execute(sqlText string, args ...interface{}) (int64, error) {
	c.executeSQL = sqlText
	c.executeArg = append([]interface{}(nil), args...)
	return 1, nil
}

func cloneHardeningMap(source map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// TestQueryEachStreamsRowsAndHonorsEarlyStop 验证 SQL 查询逐行回调、二进制值复制和提前停止语义。
func TestQueryEachStreamsRowsAndHonorsEarlyStop(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	database := NewDB(connection)
	var seen []int64
	if err := database.Table("multiple").Field("id, payload").Each(func(row map[string]interface{}) bool {
		seen = append(seen, row["id"].(int64))
		payload, ok := row["payload"].([]byte)
		if !ok || len(payload) != 2 {
			t.Fatalf("流式行的二进制字段类型错误: %#v", row["payload"])
		}
		return true
	}); err != nil {
		t.Fatalf("流式查询不应返回错误: %v", err)
	}
	if !reflect.DeepEqual(seen, []int64{1, 2}) {
		t.Fatalf("流式行顺序错误: %#v", seen)
	}

	seen = nil
	if err := database.Table("multiple").Field("id, payload").Each(func(row map[string]interface{}) bool {
		seen = append(seen, row["id"].(int64))
		return false
	}); err != nil {
		t.Fatalf("提前停止的流式查询不应返回错误: %v", err)
	}
	if !reflect.DeepEqual(seen, []int64{1}) {
		t.Fatalf("回调返回 false 后仍继续读取: %#v", seen)
	}

	seen = nil
	if err := database.Table("multiple").Join("users", "multiple.id = users.id").Field("multiple.id, multiple.payload").Each(func(row map[string]interface{}) bool {
		seen = append(seen, row["id"].(int64))
		return true
	}); err != nil {
		t.Fatalf("高级 SQL 流式查询不应返回错误: %v", err)
	}
	if !reflect.DeepEqual(seen, []int64{1, 2}) {
		t.Fatalf("高级 SQL 流式行错误: %#v", seen)
	}
}

// TestQueryEachUsesTransactionExecutor 验证事务内流式查询不会错误租用事务外连接。
func TestQueryEachUsesTransactionExecutor(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	var seen int
	err := database.Transaction(func(tx *Tx) error {
		return tx.Table("multiple").Each(func(map[string]interface{}) bool {
			seen++
			return true
		})
	})
	if err != nil {
		t.Fatalf("事务内流式查询失败: %v", err)
	}
	if seen != 2 {
		t.Fatalf("事务内应读取两行，实际为 %d", seen)
	}
}

// TestQuerySeekPageUsesTransactionExecutor 验证事务内游标分页沿用同一个事务执行器。
func TestQuerySeekPageUsesTransactionExecutor(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	err := database.Transaction(func(tx *Tx) error {
		page, err := tx.Table("multiple").Field("id,payload").SeekPage(1, "id", nil)
		if err != nil {
			return err
		}
		if len(page.List) != 1 || page.List[0]["id"] != int64(1) || page.NextCursor != int64(1) || !page.HasMore {
			return fmt.Errorf("事务内游标分页结果错误: %#v", page)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("事务内游标分页失败: %v", err)
	}
}

// TestQueryEachRejectsUnsupportedConnection 验证非 SQL 连接不会把物化 Select 冒充成流式能力。
func TestQueryEachRejectsUnsupportedConnection(t *testing.T) {
	database := NewDB(&batchRecorderConn{})
	err := database.Table("users").Each(func(map[string]interface{}) bool { return true })
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("非 SQL 连接应返回 ErrUnsupportedFeature，实际为 %v", err)
	}
}

// TestQueryEachRejectsNilCallback 验证空回调在获取连接前就明确失败。
func TestQueryEachRejectsNilCallback(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	if err := database.Table("users").Each(nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("空流式回调应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestQueryWriteOperationsDoNotMutateCallerData 验证自动时间戳只写入内部副本。
func TestQueryWriteOperationsDoNotMutateCallerData(t *testing.T) {
	connection := &queryHardeningConnection{}
	database := NewDB(connection)
	database.autoTimestamp = true

	insertData := map[string]interface{}{"name": "Ada"}
	insertBefore := cloneHardeningMap(insertData)
	if _, err := database.Table("users").Insert(insertData); err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	if !reflect.DeepEqual(insertData, insertBefore) {
		t.Fatalf("Insert 不得修改调用方数据，修改后为 %#v", insertData)
	}
	if _, ok := connection.insertData["create_time"]; !ok {
		t.Fatal("驱动收到的数据应包含创建时间戳")
	}

	updateData := map[string]interface{}{"name": "Grace"}
	updateBefore := cloneHardeningMap(updateData)
	if _, err := database.Table("users").WhereField("id", "=", 1).Update(updateData); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if !reflect.DeepEqual(updateData, updateBefore) {
		t.Fatalf("Update 不得修改调用方数据，修改后为 %#v", updateData)
	}
	if _, ok := connection.updateData["update_time"]; !ok {
		t.Fatal("驱动收到的数据应包含更新时间戳")
	}
}

// TestQueryTimestampFailureIsAtomic 验证时间戳转换失败时既不执行写入也不留下半成品数据。
func TestQueryTimestampFailureIsAtomic(t *testing.T) {
	connection := &queryHardeningConnection{}
	database := NewDB(connection)
	database.autoTimestamp = true
	database.createTimeField = "created_at"
	database.updateTimeField = "updated_at"

	data := map[string]interface{}{"created_at": int8(0), "updated_at": int64(0)}
	before := cloneHardeningMap(data)
	_, err := database.Table("users").Insert(data)
	if !errors.Is(err, ErrTimestampOverflow) {
		t.Fatalf("窄整数时间戳应返回 ErrTimestampOverflow，实际为 %v", err)
	}
	if connection.insertData != nil {
		t.Fatal("时间戳准备失败后不得调用底层驱动")
	}
	if !reflect.DeepEqual(data, before) {
		t.Fatalf("失败路径不得修改调用方数据，实际为 %#v", data)
	}
}

// TestWhereMapBuildsDeterministicConditionOrder 验证 map 条件按字段排序并保持参数对齐。
func TestWhereMapBuildsDeterministicConditionOrder(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	query := database.Table("users").WhereMap(map[string]interface{}{
		"zeta":   3,
		"alpha":  1,
		"middle": 2,
	})
	wantWhere := []string{"(alpha = ? AND middle = ? AND zeta = ?)"}
	wantArgs := []interface{}{1, 2, 3}
	if !reflect.DeepEqual(query.where, wantWhere) || !reflect.DeepEqual(query.args, wantArgs) {
		t.Fatalf("WhereMap 顺序不稳定: where=%v args=%v", query.where, query.args)
	}
}

// TestWhereSupportsSafeThinkPHPTriplet 验证字段、操作符、值三参数写法
// 经过同一操作符白名单并只绑定实际值。
func TestWhereSupportsSafeThinkPHPTriplet(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	query := database.Table("users").Where("age", ">=", 18).WhereOr("status", "<>", 0)
	if !reflect.DeepEqual(query.where, []string{"(age >= ? OR status <> ?)"}) || !reflect.DeepEqual(query.args, []interface{}{18, 0}) {
		t.Fatalf("Where 三元组编译错误: where=%v args=%v", query.where, query.args)
	}
	if _, err := database.Table("users").Where("age", "OR 1=1", 18).Select(); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("非法三元组操作符应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := database.Table("users").Where("age", 123, 18).Select(); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("非字符串三元组操作符应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestWhereFieldsQuotesValidatedIdentifiers 验证批量字段条件仍按当前 SQL 方言引用字段。
func TestWhereFieldsQuotesValidatedIdentifiers(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	query := database.Table("users").WhereFields([][]interface{}{{"users.status", "=", 1}})
	if len(query.where) != 1 || query.where[0] != `"users"."status" = ?` {
		t.Fatalf("WhereFields 字段引用错误: where=%v", query.where)
	}
}

// TestJoinDefaultsToMainTableProjection 验证 JOIN 未显式指定字段时只投影主表列，
// 防止多个表的同名列在 map 结果中发生覆盖或触发重复列错误。
func TestJoinDefaultsToMainTableProjection(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	sqlText, _, err := database.Table("orders").
		Join("users", "orders.user_id = users.id").
		BuildSelectSQL()
	if err != nil {
		t.Fatalf("构造 JOIN 查询失败: %v", err)
	}
	wantPrefix := `SELECT "orders".* FROM "orders" JOIN "users" ON "orders"."user_id" = "users"."id"`
	if !strings.HasPrefix(sqlText, wantPrefix) {
		t.Fatalf("JOIN 默认投影错误: got=%q want-prefix=%q", sqlText, wantPrefix)
	}
}

// TestQueryRejectsInvalidPagination 验证负数和乘法溢出不会进入驱动。
func TestQueryRejectsInvalidPagination(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	cases := []*Query{
		database.Table("users").Limit(-1),
		database.Table("users").Offset(-1),
		database.Table("users").Page(int(^uint(0)>>1), 2),
	}
	for index, query := range cases {
		if _, err := query.Select(); !errors.Is(err, ErrInvalidPagination) {
			t.Fatalf("第 %d 个非法分页应返回 ErrInvalidPagination，实际为 %v", index, err)
		}
	}
}

// TestQueryRejectsResourceExhaustingPagination 验证超大单页不会进入数据库驱动。
func TestQueryRejectsResourceExhaustingPagination(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	for _, query := range []*Query{
		database.Table("users").Limit(maxQueryResultRows + 1),
		database.Table("users").Page(1, maxQueryResultRows+1),
	} {
		if _, err := query.Select(); !errors.Is(err, ErrInvalidPagination) {
			t.Fatalf("超大分页应返回 ErrInvalidPagination，实际为 %v", err)
		}
	}
}

// TestQueryRejectsWhitespaceWrappedIdentifiers 验证校验值与实际执行值完全一致，
// 避免 MongoDB 等连接把首尾空白当成集合名的一部分。
func TestQueryRejectsWhitespaceWrappedIdentifiers(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	for index, query := range []*Query{
		database.Table(" users "),
		database.Table("users").WhereField(" id ", "=", 1),
	} {
		if _, err := query.Select(); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("第 %d 个空白标识符应返回 ErrInvalidQuery，实际为 %v", index, err)
		}
	}
}

// TestQueryRejectsNilContextAndPlaceholderMismatch 验证链式错误会在终端方法显式返回。
func TestQueryRejectsNilContextAndPlaceholderMismatch(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	// 类型化空上下文保留 nil 异常用例，同时明确它不是推荐调用方式。
	var nilContext context.Context
	cases := []*Query{
		database.Table("users").WithContext(nilContext),
		database.Table("users").WhereExp("score", ">", "baseline + ?", 1, 2),
		database.Table("users").WhereTime("created_at", "today", "unexpected"),
		database.Table("users").WhereRaw("", 1),
		database.Table("users").WhereRaw("id = ?", 1, 2),
		database.Table("users").HavingRaw("COUNT(id) > ?"),
		database.Table("users").Lock(true, false),
	}
	for index, query := range cases {
		if _, err := query.Select(); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("第 %d 个非法查询应返回 ErrInvalidQuery，实际为 %v", index, err)
		}
	}
}

// TestIncrementUsesBoundParameter 验证增减步长不会以内联字面量进入 SQL。
func TestIncrementUsesBoundParameter(t *testing.T) {
	connection := &queryHardeningConnection{}
	database := NewDB(connection)
	if _, err := database.Table("users").WhereField("id", "=", 7).Inc("score", 5).Update(nil); err != nil {
		t.Fatalf("自增更新失败: %v", err)
	}
	if !strings.Contains(connection.executeSQL, "score = score + ?") {
		t.Fatalf("自增步长必须参数化，实际 SQL 为 %q", connection.executeSQL)
	}
	if !reflect.DeepEqual(connection.executeArg, []interface{}{5, 7}) {
		t.Fatalf("自增参数顺序错误，实际为 %#v", connection.executeArg)
	}

	if _, err := database.Table("users").WhereField("id", "=", 7).Inc("score", 1, 2).Update(nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("多个步长应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := database.Table("users").WhereField("id", "=", 7).Dec("score", -1).Update(nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("负步长应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestIncrementRejectsConflictingAssignments 验证同一字段不会生成重复 SET 子句。
func TestIncrementRejectsConflictingAssignments(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	if _, err := database.Table("users").WhereField("id", "=", 1).Inc("score").Inc("score").Update(nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("重复 Inc 应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := database.Table("users").WhereField("id", "=", 1).Inc("score").Update(map[string]interface{}{"score": 10}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("data 与 Inc 字段冲突应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestAggregateAndCountRejectMalformedValues 验证数据库异常返回不会被静默解释为零。
func TestAggregateAndCountRejectMalformedValues(t *testing.T) {
	connection := &queryHardeningConnection{selectRows: []map[string]interface{}{{"tp_aggregate": "not-a-number"}}}
	database := NewDB(connection)
	if _, err := database.Table("users").Sum("score"); !errors.Is(err, ErrInvalidAggregateValue) {
		t.Fatalf("非法聚合值应返回 ErrInvalidAggregateValue，实际为 %v", err)
	}

	connection.selectRows = []map[string]interface{}{{"tg_count": "12.5"}}
	if _, err := database.Table("users").Distinct().Count(); !errors.Is(err, ErrInvalidAggregateValue) {
		t.Fatalf("非整数 Count 值应返回 ErrInvalidAggregateValue，实际为 %v", err)
	}
}

// TestColumnRejectsAmbiguousOrIncompleteRows 验证列读取不会静默覆盖重复键或吞掉缺失字段。
func TestColumnRejectsAmbiguousOrIncompleteRows(t *testing.T) {
	connection := &queryHardeningConnection{selectRows: []map[string]interface{}{
		{"id": int64(1), "name": "number"},
		{"id": "1", "name": "string"},
	}}
	database := NewDB(connection)
	if _, err := database.Table("users").Column("name", "id"); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("字符串化后冲突的键应返回 ErrInvalidDatabaseRow，实际为 %v", err)
	}
	if _, err := database.Table("users").Column("name", "id", "other"); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("多个 key 参数应返回 ErrInvalidQuery，实际为 %v", err)
	}

	connection.selectRows = []map[string]interface{}{{"id": 1}}
	if _, err := database.Table("users").Value("name"); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("Value 缺失目标列应返回 ErrInvalidDatabaseRow，实际为 %v", err)
	}
	if _, err := database.Table("users").Column("name"); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("缺失目标列应返回 ErrInvalidDatabaseRow，实际为 %v", err)
	}
}

// TestTodayRangeUsesCalendarBoundary 验证夏令时切换日仍以当地次日零点作为边界。
func TestTodayRangeUsesCalendarBoundary(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, location)
	start, end, err := buildTimeRange("today", now)
	if err != nil {
		t.Fatalf("构造今日范围失败: %v", err)
	}
	if start.Hour() != 0 || end.Hour() != 23 || end.Day() != 8 {
		t.Fatalf("夏令时日期边界错误: start=%v end=%v", start, end)
	}
	if end.Add(time.Second) != start.AddDate(0, 0, 1) {
		t.Fatalf("结束时间必须紧邻当地次日零点: start=%v end=%v", start, end)
	}
}

// TestSQLQueryQuotesReservedIdentifiers 验证安全查询入口在完整 SQL 中引用表、字段、条件和排序。
func TestSQLQueryQuotesReservedIdentifiers(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	connection.Builder = &builder.Pgsql{}
	database := NewDB(connection)
	_, err := database.Table("order").
		Field("select AS result").
		WhereField("select", "=", 1).
		Order("select DESC").
		Select()
	if err != nil {
		t.Fatalf("保留字查询失败: %v", err)
	}
	want := `SELECT "select" AS "result" FROM "order" WHERE "select" = $1 ORDER BY "select" DESC`
	if recorder.recordedQuery() != want {
		t.Fatalf("保留字引用错误:\n got %q\nwant %q", recorder.recordedQuery(), want)
	}
}
