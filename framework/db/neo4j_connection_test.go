package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type neo4jExecutorCall struct {
	mode   neo4j.AccessMode
	cypher string
	params map[string]interface{}
}

type fakeNeo4jExecutor struct {
	collectRecords []*neo4j.Record
	singleRecords  []*neo4j.Record
	affected       int64
	operationErr   error
	closeErr       error
	closeCalls     int
	calls          []neo4jExecutorCall
}

type contextCaptureNeo4jCloser struct {
	ctx            context.Context
	errDuringClose error
	hasDeadline    bool
}

func (closer *contextCaptureNeo4jCloser) Close(ctx context.Context) error {
	closer.ctx = ctx
	closer.errDuringClose = ctx.Err()
	_, closer.hasDeadline = ctx.Deadline()
	return closer.errDuringClose
}

func (executor *fakeNeo4jExecutor) record(mode neo4j.AccessMode, cypher string, params map[string]interface{}) {
	executor.calls = append(executor.calls, neo4jExecutorCall{mode: mode, cypher: cypher, params: cloneDatabaseMap(params)})
}

func (executor *fakeNeo4jExecutor) Collect(_ context.Context, mode neo4j.AccessMode, cypher string, params map[string]interface{}) ([]*neo4j.Record, error) {
	executor.record(mode, cypher, params)
	return executor.collectRecords, executor.operationErr
}

func (executor *fakeNeo4jExecutor) Single(_ context.Context, mode neo4j.AccessMode, cypher string, params map[string]interface{}) (*neo4j.Record, error) {
	executor.record(mode, cypher, params)
	if executor.operationErr != nil {
		return nil, executor.operationErr
	}
	if len(executor.singleRecords) == 0 {
		return nil, nil
	}
	record := executor.singleRecords[0]
	executor.singleRecords = executor.singleRecords[1:]
	return record, nil
}

func (executor *fakeNeo4jExecutor) Execute(_ context.Context, mode neo4j.AccessMode, cypher string, params map[string]interface{}) (int64, error) {
	executor.record(mode, cypher, params)
	return executor.affected, executor.operationErr
}

func (executor *fakeNeo4jExecutor) Close(context.Context) error {
	executor.closeCalls++
	return executor.closeErr
}

// TestNeo4jBuildCypherWhereRejectsUnparseable 验证无法解析的条件会返回错误，
// 避免更新/删除时静默丢弃 WHERE 造成整类节点被误操作。
func TestNeo4jBuildCypherWhereRejectsUnparseable(t *testing.T) {
	c := &Neo4jConnection{}

	if _, _, err := c.buildCypherWhere([]string{"weird_unparseable_clause"}, nil); err == nil {
		t.Fatal("Neo4j 无法解析的条件必须返回错误，不能静默跳过")
	}
	if _, _, err := c.buildCypherWhere([]string{"age = ?"}, nil); err == nil {
		t.Fatal("Neo4j 条件参数不足必须返回错误")
	}
}

// TestNeo4jBuildCypherWhereSupportsCollectionAndRangeConditions 验证统一查询层
// 产生的 IN、LIKE、BETWEEN 条件在 Neo4j 中不会退化成空 WHERE。
func TestNeo4jBuildCypherWhereSupportsCollectionAndRangeConditions(t *testing.T) {
	c := &Neo4jConnection{}

	clause, params, err := c.buildCypherWhere(
		[]string{"id IN (?, ?)", "name LIKE ?", "age BETWEEN ? AND ?"},
		[]interface{}{int64(1), int64(2), "%Admin_", 18, 30},
	)
	if err != nil {
		t.Fatalf("Neo4j 应支持上层查询生成的集合与区间条件: %v", err)
	}

	expectedFragments := []string{
		"n.`id` IN $w0",
		"n.`name` =~ $w1",
		"n.`age` >= $w2_start AND n.`age` <= $w2_end",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(clause, fragment) {
			t.Fatalf("Neo4j WHERE 缺少片段 %q，实际为 %q", fragment, clause)
		}
	}
	if !reflect.DeepEqual(params["w0"], []interface{}{int64(1), int64(2)}) {
		t.Fatalf("IN 参数应聚合为切片，实际为 %#v", params["w0"])
	}
	if params["w1"] != "(?i)^.*Admin.$" {
		t.Fatalf("LIKE 参数应转换为大小写不敏感的安全正则，实际为 %#v", params["w1"])
	}
	if params["w2_start"] != 18 || params["w2_end"] != 30 {
		t.Fatalf("BETWEEN 参数应拆分为起止值，实际为 %#v/%#v", params["w2_start"], params["w2_end"])
	}
}

// TestNeo4jBuildCypherWhereSupportsOrAndImpossiblePredicate 验证统一查询器的 OR 与空 IN 条件。
func TestNeo4jBuildCypherWhereSupportsOrAndImpossiblePredicate(t *testing.T) {
	connection := &Neo4jConnection{}
	clause, params, err := connection.buildCypherWhere(
		[]string{"(status = ? OR owner_id = ?)", "1 = 0"},
		[]interface{}{"open", int64(7)},
	)
	if err != nil {
		t.Fatalf("构建 Neo4j 布尔条件失败: %v", err)
	}
	if !strings.Contains(clause, "(n.`status` = $w0 OR n.`owner_id` = $w1)") || !strings.Contains(clause, "false") {
		t.Fatalf("Neo4j 布尔条件错误: %q", clause)
	}
	if params["w0"] != "open" || params["w1"] != int64(7) {
		t.Fatalf("Neo4j OR 参数错误: %#v", params)
	}
}

// TestNeo4jBuildCypherWhereRejectsExtraArgs 验证多余参数不会被静默忽略。
func TestNeo4jBuildCypherWhereRejectsExtraArgs(t *testing.T) {
	connection := &Neo4jConnection{}
	_, _, err := connection.buildCypherWhere([]string{"age = ?"}, []interface{}{18, 19})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("多余参数应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestNeo4jBuildCypherWhereCoversNullNegationAndComparisons 验证空值、否定集合、
// 否定模糊匹配及全部比较符均保持参数化，非法占位符和超长模式被拒绝。
func TestNeo4jBuildCypherWhereCoversNullNegationAndComparisons(t *testing.T) {
	connection := &Neo4jConnection{}
	clause, params, err := connection.buildCypherWhere(
		[]string{
			"deleted_at IS NULL",
			"email IS NOT NULL",
			"status NOT IN (?, ?)",
			"name NOT LIKE ?",
			"score <> ?",
			"level > ?",
			"rank < ?",
			"quota != ?",
		},
		[]interface{}{"draft", "deleted", "%bot%", 0, 1, 10, 20},
	)
	if err != nil {
		t.Fatalf("构建 Neo4j 完整条件失败: %v", err)
	}
	for _, fragment := range []string{
		"n.`deleted_at` IS NULL",
		"n.`email` IS NOT NULL",
		"NOT (n.`status` IN $w0)",
		"NOT (n.`name` =~ $w1)",
		"n.`score` <> $w2",
		"n.`level` > $w3",
		"n.`rank` < $w4",
		"n.`quota` <> $w5",
	} {
		if !strings.Contains(clause, fragment) {
			t.Fatalf("Neo4j 完整条件缺少 %q: %q", fragment, clause)
		}
	}
	if len(params) != 6 {
		t.Fatalf("Neo4j 参数聚合数量错误: %#v", params)
	}

	invalid := []struct {
		where string
		args  []interface{}
	}{
		{where: "id IN ?", args: []interface{}{1}},
		{where: "name LIKE (?, ?)", args: []interface{}{"a", "b"}},
		{where: "name LIKE ?", args: []interface{}{strings.Repeat("a", maxCypherLikeLength+1)}},
		{where: "(id = ?", args: []interface{}{1}},
		{where: "id = ? AND ", args: []interface{}{1}},
	}
	for index, testCase := range invalid {
		if _, _, err := connection.buildCypherWhere([]string{testCase.where}, testCase.args); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("第 %d 个非法 Neo4j 条件应返回 ErrInvalidQuery，实际为 %v", index, err)
		}
	}
}

// TestNeo4jConnectionExecutesParameterizedOperations 验证所有统一 CRUD 操作均通过窄执行器、
// 使用正确访问模式和参数化 Cypher，并返回真实影响行数。
func TestNeo4jConnectionExecutesParameterizedOperations(t *testing.T) {
	executor := &fakeNeo4jExecutor{
		collectRecords: []*neo4j.Record{{Keys: []string{"username"}, Values: []interface{}{"张三"}}},
		singleRecords: []*neo4j.Record{
			{Keys: []string{"count"}, Values: []interface{}{int64(1)}},
			{Keys: []string{"count"}, Values: []interface{}{int64(2)}},
			{Keys: []string{"count"}, Values: []interface{}{int64(2)}},
		},
		affected: 3,
	}
	connection := &Neo4jConnection{executor: executor}

	rows, err := connection.Select("users", "name AS username", []string{"id = ?"}, []interface{}{int64(7)}, "name DESC", 10, 1)
	if err != nil || len(rows) != 1 || rows[0]["username"] != "张三" {
		t.Fatalf("Neo4j 查询结果错误: rows=%#v err=%v", rows, err)
	}
	data := map[string]interface{}{"name": "张三"}
	if affected, err := connection.Insert("users", data); err != nil || affected != 1 {
		t.Fatalf("Neo4j 插入结果错误: affected=%d err=%v", affected, err)
	}
	if affected, err := connection.Update("users", map[string]interface{}{"active": true}, []string{"id = ?"}, []interface{}{7}); err != nil || affected != 2 {
		t.Fatalf("Neo4j 更新结果错误: affected=%d err=%v", affected, err)
	}
	if affected, err := connection.Delete("users", []string{"active = ?"}, []interface{}{false}); err != nil || affected != 3 {
		t.Fatalf("Neo4j 删除结果错误: affected=%d err=%v", affected, err)
	}
	if count, err := connection.Count("users", []string{"active = ?"}, []interface{}{true}); err != nil || count != 2 {
		t.Fatalf("Neo4j 计数结果错误: count=%d err=%v", count, err)
	}

	if len(executor.calls) != 5 {
		t.Fatalf("Neo4j 执行次数错误: %d", len(executor.calls))
	}
	if executor.calls[0].mode != neo4j.AccessModeRead || !strings.Contains(executor.calls[0].cypher, "MATCH (n:`users`)") || executor.calls[0].params["w0"] != int64(7) {
		t.Fatalf("Neo4j 查询命令错误: %#v", executor.calls[0])
	}
	if executor.calls[1].mode != neo4j.AccessModeWrite || !strings.Contains(executor.calls[1].cypher, "CREATE (n:`users` $props)") {
		t.Fatalf("Neo4j 插入命令错误: %#v", executor.calls[1])
	}
	if executor.calls[3].mode != neo4j.AccessModeWrite || !strings.Contains(executor.calls[3].cypher, "DETACH DELETE n") {
		t.Fatalf("Neo4j 删除必须使用单条 DETACH DELETE: %#v", executor.calls[3])
	}
	data["name"] = "李四"
	props := executor.calls[1].params["props"].(map[string]interface{})
	if props["name"] != "张三" {
		t.Fatalf("Neo4j 插入参数必须与调用方数据隔离: %#v", props)
	}
}

// TestNeo4jConnectionRejectsInvalidRowsAndClosesOnce 验证非法聚合结果不会被吞掉，
// 且执行器关闭错误在重复关闭时保持稳定。
func TestNeo4jConnectionRejectsInvalidRowsAndClosesOnce(t *testing.T) {
	executor := &fakeNeo4jExecutor{singleRecords: []*neo4j.Record{{Values: []interface{}{"invalid"}}}, closeErr: errors.New("close failed")}
	connection := &Neo4jConnection{executor: executor}
	if _, err := connection.Count("users", nil, nil); !errors.Is(err, ErrInvalidAggregateValue) {
		t.Fatalf("非法计数应返回 ErrInvalidAggregateValue，实际为 %v", err)
	}
	first := connection.Close()
	second := connection.Close()
	if first == nil || first != second || executor.closeCalls != 1 {
		t.Fatalf("Neo4j 关闭应幂等且保留错误: first=%v second=%v calls=%d", first, second, executor.closeCalls)
	}
}

// TestNeo4jConnectionRejectsDuplicateProjectionAliases 验证重复别名在执行前被拒绝，
// 驱动异常返回重复列键时也不会静默覆盖前一列。
func TestNeo4jConnectionRejectsDuplicateProjectionAliases(t *testing.T) {
	executor := &fakeNeo4jExecutor{}
	connection := &Neo4jConnection{executor: executor}
	if _, err := connection.Select("users", "name AS value,email AS value", nil, nil, "", 0, 0); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("重复投影别名应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatal("重复投影别名不得访问 Neo4j 执行器")
	}

	executor.collectRecords = []*neo4j.Record{{Keys: []string{"name", "name"}, Values: []interface{}{"Ada", "Grace"}}}
	if _, err := connection.Select("users", "name,email", nil, nil, "", 0, 0); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("驱动重复列键应返回 ErrInvalidDatabaseRow，实际为 %v", err)
	}
}

// TestNeo4jDriverExecutorHonorsCancelledContext 验证生产适配层为每种执行模式创建会话，
// 并在请求已取消时立即返回错误而不等待网络连接。
func TestNeo4jDriverExecutorHonorsCancelledContext(t *testing.T) {
	driver, err := neo4j.NewDriverWithContext("bolt://127.0.0.1:1", neo4j.NoAuth())
	if err != nil {
		t.Fatalf("创建本地 Neo4j 驱动失败: %v", err)
	}
	executor := &neo4jDriverExecutor{driver: driver, database: "neo4j"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := executor.Collect(ctx, neo4j.AccessModeRead, "RETURN 1", nil); err == nil {
		t.Fatal("已取消上下文的 Collect 必须失败")
	}
	if _, err := executor.Single(ctx, neo4j.AccessModeRead, "RETURN 1", nil); err == nil {
		t.Fatal("已取消上下文的 Single 必须失败")
	}
	if _, err := executor.Execute(ctx, neo4j.AccessModeWrite, "CREATE (n)", nil); err == nil {
		t.Fatal("已取消上下文的 Execute 必须失败")
	}
	if err := executor.Close(context.Background()); err != nil {
		t.Fatalf("关闭未建连 Neo4j 驱动失败: %v", err)
	}
	if _, err := (&neo4jDriverExecutor{}).session(context.Background(), neo4j.AccessModeRead); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("空生产适配器应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
	if err := (&neo4jDriverExecutor{}).Close(context.Background()); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("关闭空生产适配器应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
}

// TestCloseNeo4jSessionUsesIndependentDeadline 验证业务上下文取消后，
// 会话清理仍获得独立的有界上下文以归还连接池资源。
func TestCloseNeo4jSessionUsesIndependentDeadline(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	closer := &contextCaptureNeo4jCloser{}
	if err := closeNeo4jSession(parent, closer); err != nil {
		t.Fatalf("独立会话清理上下文不应继承取消状态: %v", err)
	}
	if closer.ctx == nil || closer.errDuringClose != nil {
		t.Fatalf("会话清理上下文在 Close 执行期间必须可用: %#v", closer.ctx)
	}
	if !closer.hasDeadline {
		t.Fatal("会话清理上下文必须有截止时间")
	}
}
