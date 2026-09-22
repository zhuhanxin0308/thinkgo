package db

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"
)

// TestPaginationValueObjects 验证分页结果对象在空指针和正常值下都保持稳定的响应结构。
func TestPaginationValueObjects(t *testing.T) {
	var nilCursor *CursorPage[map[string]any]
	cursorMap := nilCursor.ToMap()
	if cursorMap["page_size"] != DefaultPageSize || cursorMap["has_more"] != false {
		t.Fatalf("空游标分页默认值错误: %#v", cursorMap)
	}
	cursor := (&CursorPage[map[string]any]{List: []map[string]interface{}{{"id": 1}}, NextCursor: 1, PageSize: 1, HasMore: true}).ToMap()
	if cursor["next_cursor"] != 1 || cursor["page_size"] != 1 || cursor["has_more"] != true {
		t.Fatalf("游标分页响应字段错误: %#v", cursor)
	}

	var nilPaginator *Paginator[map[string]any]
	paginatorMap := nilPaginator.ToMap()
	if paginatorMap["page"] != 1 || paginatorMap["page_size"] != DefaultPageSize || paginatorMap["last_page"] != 0 {
		t.Fatalf("空偏移分页默认值错误: %#v", paginatorMap)
	}
	paginator, err := buildPaginator[map[string]any](nil, 5, 2, 2)
	if err != nil || paginator.LastPage != 3 || !paginator.HasMore {
		t.Fatalf("分页对象构造结果错误: paginator=%#v err=%v", paginator, err)
	}
	if offset, err := (&Paginator[map[string]any]{Page: 1, PageSize: 20}).Offset(); err != nil || offset != 0 {
		t.Fatalf("第一页偏移量错误: offset=%d err=%v", offset, err)
	}
	if _, err := buildPaginator[map[string]any](nil, -1, 1, 20); !errors.Is(err, ErrInvalidPagination) {
		t.Fatalf("负总数应拒绝: %v", err)
	}
}

// TestPredicateAndCursorValidationContracts 验证谓词副本和游标单调性，防止调用方篡改查询状态。
func TestPredicateAndCursorValidationContracts(t *testing.T) {
	predicate := newPredicate()
	if !predicate.Empty() {
		t.Fatal("新谓词应为空")
	}
	predicate = predicate.appendValidated("id = ?", []interface{}{1})
	if predicate.Empty() {
		t.Fatal("追加条件后的谓词不应为空")
	}
	clauses := predicate.Clauses()
	clauses[0].Args[0] = 99
	if predicate.Clauses()[0].Args[0] != 1 {
		t.Fatal("谓词参数必须返回防御性副本")
	}
	raw := newPredicate().appendRaw("id = ?", []interface{}{1})
	if _, err := raw.PortableNodes(); !errors.Is(err, ErrUnsafeExpression) {
		t.Fatalf("非 SQL 驱动不应接受显式 Raw 谓词: %v", err)
	}

	codec := OrderedCursorCodec{}
	validRows := []map[string]interface{}{{"id": int64(1)}, {"id": int64(2)}}
	if err := validateCursorRows(validRows, "id", nil, codec); err != nil {
		t.Fatalf("递增游标行不应失败: %v", err)
	}
	for _, rows := range [][]map[string]interface{}{
		{{"name": "missing-id"}},
		{{"id": int64(2)}, {"id": int64(1)}},
	} {
		if err := validateCursorRows(rows, "id", nil, codec); !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("非法游标行应被拒绝: rows=%#v err=%v", rows, err)
		}
	}
	if err := validateCursorRows([]map[string]interface{}{{"id": int64(2)}}, "id", int64(2), codec); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("不大于前游标的结果应被拒绝: %v", err)
	}
	if err := validateCursorBoundary(nil, codec); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("空游标边界应被拒绝: %v", err)
	}
}

// TestQueryBudgetAndChunkLimitContracts 验证查询参数预算和批处理 look-ahead 不会发生整数溢出。
func TestQueryBudgetAndChunkLimitContracts(t *testing.T) {
	if err := validateArgumentBudget(2, 3, 5); err != nil {
		t.Fatalf("预算内参数不应失败: %v", err)
	}
	for _, values := range [][3]int{{-1, 0, 5}, {5, 1, 5}, {2, 4, 5}} {
		if err := validateArgumentBudget(values[0], values[1], values[2]); !errors.Is(err, ErrQueryArgumentsTooMany) {
			t.Fatalf("超预算参数应被拒绝: values=%v err=%v", values, err)
		}
	}
	if err := validateBindParameterBudget(nil, 1, 2); err != nil {
		t.Fatalf("合法绑定参数不应失败: %v", err)
	}
	if err := validateBindParameterBudget(nil, -1); !errors.Is(err, ErrQueryArgumentsTooMany) {
		t.Fatalf("负绑定参数应被拒绝: %v", err)
	}
	if limit, lookAhead := chunkReadLimit(100); limit != 101 || !lookAhead {
		t.Fatalf("批处理 look-ahead 计算错误: limit=%d lookAhead=%t", limit, lookAhead)
	}
	maxInt := int(^uint(0) >> 1)
	if limit, lookAhead := chunkReadLimit(maxInt); limit != maxInt || lookAhead {
		t.Fatalf("最大整数批处理不应溢出: limit=%d lookAhead=%t", limit, lookAhead)
	}
	if _, err := buildPaginator[map[string]any](nil, math.MaxInt64, maxInt, maxInt); err == nil {
		t.Fatal("超出 int 页码范围的分页应失败")
	}
}

// TestQueryColumnUsesDirectSQLScanner 验证 SQL Column 只物化目标列，避免构造完整行 map。
func TestQueryColumnUsesDirectSQLScanner(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	result, err := database.Table("users").Column("value")
	if err != nil {
		t.Fatalf("SQL Column 执行失败: %v", err)
	}
	values, ok := result.([]interface{})
	if !ok || len(values) != 1 || values[0] != int64(1) {
		t.Fatalf("SQL Column 结果错误: %#v", result)
	}
}

// TestDatabaseMetadataAndPartialWriteContracts 验证 SQL 元数据、返回值扫描和部分写入错误的公开契约。
func TestDatabaseMetadataAndPartialWriteContracts(t *testing.T) {
	var nilConnection *SQLConnection
	nilConnection.SetLocation(time.UTC)
	if nilConnection.Location() != time.UTC {
		t.Fatalf("空 SQL 连接应安全回退 UTC: %v", nilConnection.Location())
	}
	connection := &SQLConnection{}
	connection.SetLocation(nil)
	if connection.Location() != time.UTC {
		t.Fatalf("空时区应回退 UTC: %v", connection.Location())
	}
	connection.SetLocation(time.UTC)
	if connection.Location() != time.UTC {
		t.Fatalf("SQL 连接未保存时区: %v", connection.Location())
	}
	if len(connection.Capabilities().InsertIDKinds) != 1 || connection.Capabilities().InsertIDKinds[0] != InsertIDInteger {
		t.Fatalf("默认 SQL 能力声明错误: %#v", connection.Capabilities())
	}
	postgres := &SQLConnection{Builder: &builder.Pgsql{}}
	if len(postgres.Capabilities().InsertIDKinds) != 3 {
		t.Fatalf("PostgreSQL 应允许字符串和动态 ID: %#v", postgres.Capabilities())
	}

	if _, err := sqlOutputValue(nil); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("空 RETURNING 目标应拒绝: %v", err)
	}
	if _, err := sqlOutputValue(1); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("非指针 RETURNING 目标应拒绝: %v", err)
	}
	destination := int64(7)
	value, err := sqlOutputValue(&destination)
	if err != nil || value != int64(7) {
		t.Fatalf("合法 RETURNING 目标扫描错误: value=%v err=%v", value, err)
	}

	var nilPartial *PartialWriteError
	if !errors.Is(nilPartial, ErrPartialWrite) || nilPartial.Error() != ErrPartialWrite.Error() || nilPartial.Unwrap() != nil {
		t.Fatalf("空部分写入错误包装器结果错误: error=%v unwrap=%v", nilPartial, nilPartial.Unwrap())
	}
	cause := errors.New("assign failed")
	partial := &PartialWriteError{Cause: cause}
	if !errors.Is(partial, ErrPartialWrite) || !errors.Is(partial, cause) {
		t.Fatalf("部分写入错误应保留根因: %v", partial)
	}
}

// TestQueryDetachDeleteAndModelEachContracts 验证删除关系标志和模型流式读取的委托路径。
func TestQueryDetachDeleteAndModelEachContracts(t *testing.T) {
	database := NewDB(&mockConnection{})
	deleted, err := database.Table("users").Where("id = ?", 1).DetachDelete()
	if err != nil || deleted != 1 {
		t.Fatalf("DetachDelete 委托结果错误: deleted=%d err=%v", deleted, err)
	}
	model := NewModel(database, "users")
	callbackCalled := false
	err = model.Each(func(row map[string]interface{}) bool {
		callbackCalled = true
		return row["table"] == "users"
	})
	if !errors.Is(err, ErrUnsupportedFeature) || callbackCalled {
		t.Fatalf("非流式驱动应明确拒绝 Model.Each: called=%t err=%v", callbackCalled, err)
	}
	modelQuery := model.newModelQuery().WhereTimeAs("created_at", TimestampValueTypeUnix, "between", time.Unix(1, 0), time.Unix(2, 0))
	if modelQuery == nil || modelQuery.query == nil || len(modelQuery.query.where) == 0 {
		t.Fatal("ModelQuery.WhereTimeAs 应保留时间条件")
	}
}

// TestQueryArgumentAppendContracts 验证单参数和双参数追加都遵守统一预算。
func TestQueryArgumentAppendContracts(t *testing.T) {
	query := NewDB(&mockConnection{}).Table("users")
	var target []interface{}
	if !query.appendQueryArgument(&target, 1) || !query.appendTwoQueryArguments(&target, 2, 3) {
		t.Fatalf("合法参数追加失败: %#v err=%v", target, query.err)
	}
	if len(target) != 3 || target[2] != 3 {
		t.Fatalf("参数追加结果错误: %#v", target)
	}
	query.args = make([]interface{}, maxQueryArguments)
	if query.appendQueryArgument(&target, 4) || query.err == nil {
		t.Fatal("超过预算的单参数追加必须失败")
	}
	query.err = nil
	query.args = make([]interface{}, maxQueryArguments-1)
	if query.appendTwoQueryArguments(&target, 4, 5) || query.err == nil {
		t.Fatal("超过预算的双参数追加必须失败")
	}
}
