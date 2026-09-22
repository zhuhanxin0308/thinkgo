package neo4j

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
	relatedDeleted int64
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

type managedExecutorResult struct {
	neo4j.ResultWithContext
	record  *neo4j.Record
	records []*neo4j.Record
	summary neo4j.ResultSummary
}

func (result *managedExecutorResult) Single(context.Context) (*neo4j.Record, error) {
	return result.record, nil
}

func (result *managedExecutorResult) Collect(context.Context) ([]*neo4j.Record, error) {
	return result.records, nil
}

func (result *managedExecutorResult) Consume(context.Context) (neo4j.ResultSummary, error) {
	return result.summary, nil
}

type managedExecutorCounters struct {
	neo4j.Counters
	nodesDeleted         int
	relationshipsDeleted int
}

func (counters *managedExecutorCounters) NodesDeleted() int {
	return counters.nodesDeleted
}

func (counters *managedExecutorCounters) RelationshipsDeleted() int {
	return counters.relationshipsDeleted
}

type managedExecutorSummary struct {
	neo4j.ResultSummary
	counters neo4j.Counters
}

func (summary *managedExecutorSummary) Counters() neo4j.Counters {
	return summary.counters
}

type managedExecutorTransaction struct {
	neo4j.ManagedTransaction
	session *managedExecutorSession
}

func (transaction *managedExecutorTransaction) Run(_ context.Context, cypher string, params map[string]interface{}) (neo4j.ResultWithContext, error) {
	transaction.session.transactionRunCalls++
	transaction.session.lastCypher = cypher
	transaction.session.lastParams = cloneDatabaseMap(params)
	return transaction.session.result, nil
}

type managedExecutorSession struct {
	neo4j.SessionWithContext
	result              neo4j.ResultWithContext
	executeReadCalls    int
	executeWriteCalls   int
	transactionRunCalls int
	autoCommitRunCalls  int
	closeCalls          int
	lastCypher          string
	lastParams          map[string]interface{}
}

func (session *managedExecutorSession) ExecuteRead(ctx context.Context, work neo4j.ManagedTransactionWork, _ ...func(*neo4j.TransactionConfig)) (interface{}, error) {
	session.executeReadCalls++
	return work(&managedExecutorTransaction{session: session})
}

func (session *managedExecutorSession) ExecuteWrite(ctx context.Context, work neo4j.ManagedTransactionWork, _ ...func(*neo4j.TransactionConfig)) (interface{}, error) {
	session.executeWriteCalls++
	return work(&managedExecutorTransaction{session: session})
}

func (session *managedExecutorSession) Run(context.Context, string, map[string]interface{}, ...func(*neo4j.TransactionConfig)) (neo4j.ResultWithContext, error) {
	session.autoCommitRunCalls++
	return session.result, nil
}

func (session *managedExecutorSession) Close(context.Context) error {
	session.closeCalls++
	return nil
}

type managedExecutorDriver struct {
	neo4j.DriverWithContext
	session    neo4j.SessionWithContext
	lastConfig neo4j.SessionConfig
}

func (driver *managedExecutorDriver) NewSession(_ context.Context, config neo4j.SessionConfig) neo4j.SessionWithContext {
	driver.lastConfig = config
	return driver.session
}

func TestNeoExecutorUsesManagedTransactions(t *testing.T) {
	record := &neo4j.Record{Keys: []string{"count"}, Values: []interface{}{int64(1)}}
	session := &managedExecutorSession{result: &managedExecutorResult{record: record}}
	driver := &managedExecutorDriver{session: session}
	executor := &neo4jDriverExecutor{driver: driver, database: "tenant_a"}

	actual, err := executor.Single(context.Background(), neo4j.AccessModeWrite, "CREATE (n) RETURN count(n)", map[string]interface{}{"name": "Ada"})
	if err != nil || actual != record {
		t.Fatalf("managed Single failed: record=%#v err=%v", actual, err)
	}
	if session.executeWriteCalls != 1 || session.executeReadCalls != 0 || session.transactionRunCalls != 1 || session.autoCommitRunCalls != 0 {
		t.Fatalf("executor did not use managed write transaction: %#v", session)
	}
	if driver.lastConfig.DatabaseName != "tenant_a" || session.closeCalls != 1 {
		t.Fatalf("executor lost target database or session cleanup: config=%#v closes=%d", driver.lastConfig, session.closeCalls)
	}
}

func TestNeoExecutorManagedModesCoverCollectAndExecute(t *testing.T) {
	records := []*neo4j.Record{{Keys: []string{"name"}, Values: []interface{}{"Ada"}}}
	session := &managedExecutorSession{result: &managedExecutorResult{records: records}}
	executor := &neo4jDriverExecutor{driver: &managedExecutorDriver{session: session}, database: "tenant_a"}

	actual, err := executor.Collect(context.Background(), neo4j.AccessModeRead, "MATCH (n) RETURN n.name", nil)
	if err != nil || !reflect.DeepEqual(actual, records) {
		t.Fatalf("managed Collect failed: records=%#v err=%v", actual, err)
	}
	session.result = &managedExecutorResult{summary: &managedExecutorSummary{counters: &managedExecutorCounters{
		nodesDeleted: 2, relationshipsDeleted: 3,
	}}}
	deleted, err := executor.Execute(context.Background(), neo4j.AccessModeWrite, "MATCH (n) DETACH DELETE n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Deleted != 2 || deleted.RelatedDeleted != 3 || !deleted.RelatedDeletedKnown {
		t.Fatalf("managed Execute lost counters: %#v", deleted)
	}
	if session.executeReadCalls != 1 || session.executeWriteCalls != 1 || session.transactionRunCalls != 2 || session.autoCommitRunCalls != 0 || session.closeCalls != 2 {
		t.Fatalf("managed modes were not used consistently: %#v", session)
	}
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

func (executor *fakeNeo4jExecutor) Execute(_ context.Context, mode neo4j.AccessMode, cypher string, params map[string]interface{}) (DeleteResult, error) {
	executor.record(mode, cypher, params)
	return DeleteResult{
		Deleted:             executor.affected,
		RelatedDeleted:      executor.relatedDeleted,
		RelatedDeletedKnown: true,
	}, executor.operationErr
}

func (executor *fakeNeo4jExecutor) Close(context.Context) error {
	executor.closeCalls++
	return executor.closeErr
}

func TestNeoDeleteModesAreExplicit(t *testing.T) {
	executor := &fakeNeo4jExecutor{affected: 2, relatedDeleted: 3}
	connection := &Neo4jConnection{executor: executor}
	predicate := mustTestPredicate(t, []string{"id = ?"}, []interface{}{int64(7)})

	strict, err := connection.Delete(context.Background(), newDeleteRequest("users", predicate, "id", false))
	if err != nil {
		t.Fatal(err)
	}
	strictCypher := executor.calls[len(executor.calls)-1].cypher
	if strict.Deleted != 2 || !strings.Contains(strictCypher, " DELETE n") || strings.Contains(strictCypher, "DETACH") {
		t.Fatalf("strict delete used detach semantics: result=%#v cypher=%q", strict, strictCypher)
	}

	detached, err := connection.Delete(context.Background(), newDeleteRequest("users", predicate, "id", true))
	if err != nil {
		t.Fatal(err)
	}
	detachCypher := executor.calls[len(executor.calls)-1].cypher
	if !strings.Contains(detachCypher, "DETACH DELETE n") || !detached.RelatedDeletedKnown || detached.RelatedDeleted != 3 {
		t.Fatalf("detach delete lost explicit semantics: result=%#v cypher=%q", detached, detachCypher)
	}
}

func TestQueryDeleteDefaultsToStrictAndRequiresExplicitDetach(t *testing.T) {
	executor := &fakeNeo4jExecutor{affected: 1, relatedDeleted: 2}
	database := NewDB(&Neo4jConnection{executor: executor})
	query := database.Name("users").WhereField("id", "=", int64(7))

	if deleted, err := query.Delete(); err != nil || deleted != 1 {
		t.Fatalf("strict Query.Delete failed: deleted=%d err=%v", deleted, err)
	}
	if cypher := executor.calls[len(executor.calls)-1].cypher; strings.Contains(cypher, "DETACH") {
		t.Fatalf("Query.Delete must remain strict: %q", cypher)
	}
	result, err := query.DetachDeleteResult()
	if err != nil {
		t.Fatal(err)
	}
	if cypher := executor.calls[len(executor.calls)-1].cypher; !strings.Contains(cypher, "DETACH DELETE n") {
		t.Fatalf("DetachDeleteResult did not request detach semantics: %q", cypher)
	}
	if result.Deleted != 1 || result.RelatedDeleted != 2 || !result.RelatedDeletedKnown {
		t.Fatalf("DetachDeleteResult lost counters: %#v", result)
	}
}

func TestNeoRejectsDottedPropertyOnEveryWrite(t *testing.T) {
	executor := &fakeNeo4jExecutor{singleRecords: []*neo4j.Record{
		{Keys: []string{"count"}, Values: []interface{}{int64(1)}},
		{Keys: []string{"count"}, Values: []interface{}{int64(1)}},
	}}
	connection := &Neo4jConnection{executor: executor}
	predicate := mustTestPredicate(t, []string{"id = ?"}, []interface{}{int64(7)})
	tests := []struct {
		name    string
		execute func() error
	}{
		{
			name: "insert",
			execute: func() error {
				_, err := connection.Insert(context.Background(), newInsertRequest("users", map[string]interface{}{"profile.name": "Ada"}, "id", false))
				return err
			},
		},
		{
			name: "update",
			execute: func() error {
				_, err := connection.Update(context.Background(), newUpdateRequest("users", map[string]interface{}{"profile.name": "Ada"}, predicate, "id"))
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.execute(); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("dotted Neo4j property must be rejected: %v", err)
			}
		})
	}
	if len(executor.calls) != 0 {
		t.Fatalf("invalid properties must fail before executor access: %#v", executor.calls)
	}
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

	rows, err := connection.Select(context.Background(), newSelectRequest(
		"users", "name AS username", mustTestPredicate(t, []string{"id = ?"}, []interface{}{int64(7)}), "id", "name DESC", 10, 1, nil,
	))
	if err != nil || len(rows) != 1 || rows[0]["username"] != "张三" {
		t.Fatalf("Neo4j 查询结果错误: rows=%#v err=%v", rows, err)
	}
	data := map[string]interface{}{"name": "张三"}
	if result, err := connection.Insert(context.Background(), newInsertRequest("users", data, "id", false)); err != nil || result.Affected != 1 {
		affected := result.Affected
		t.Fatalf("Neo4j 插入结果错误: affected=%d err=%v", affected, err)
	}
	if result, err := connection.Update(context.Background(), newUpdateRequest("users", map[string]interface{}{"active": true}, mustTestPredicate(t, []string{"id = ?"}, []interface{}{7}), "id")); err != nil || result.Count() != 2 {
		affected := result.Count()
		t.Fatalf("Neo4j 更新结果错误: affected=%d err=%v", affected, err)
	}
	if result, err := connection.Delete(context.Background(), newDeleteRequest("users", mustTestPredicate(t, []string{"active = ?"}, []interface{}{false}), "id", true)); err != nil || result.Deleted != 3 {
		affected := result.Deleted
		t.Fatalf("Neo4j 删除结果错误: affected=%d err=%v", affected, err)
	}
	if count, err := connection.Count(context.Background(), newCountRequest("users", mustTestPredicate(t, []string{"active = ?"}, []interface{}{true}), "id")); err != nil || count != 2 {
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

func TestNeo4jOperationUsesApplicationPrimaryKeyAsInsertedID(t *testing.T) {
	executor := &fakeNeo4jExecutor{singleRecords: []*neo4j.Record{
		{Keys: []string{"count"}, Values: []interface{}{int64(1)}},
		{Keys: []string{"count"}, Values: []interface{}{int64(2)}},
	}}
	connection := &Neo4jConnection{executor: executor}

	inserted, err := connection.Insert(context.Background(), newInsertRequest(
		"users", map[string]interface{}{"uuid": "u-1", "name": "Ada"}, "uuid", true,
	))
	if err != nil {
		t.Fatalf("Neo4j typed insert failed: %v", err)
	}
	if inserted.Affected != 1 || !inserted.IDKnown || inserted.ID != "u-1" {
		t.Fatalf("Neo4j insert must return the application primary key: %#v", inserted)
	}

	predicate := mustTestPredicate(t, []string{"uuid = ?"}, []interface{}{"u-1"})
	updated, err := connection.Update(context.Background(), newUpdateRequest(
		"users", map[string]interface{}{"active": true}, predicate, "uuid",
	))
	if err != nil {
		t.Fatalf("Neo4j typed update failed: %v", err)
	}
	if updated.Affected != 2 || updated.Matched != 2 || !updated.MatchedKnown || updated.ModifiedKnown {
		t.Fatalf("Neo4j update capability semantics are wrong: %#v", updated)
	}
}

func TestNeo4jOperationRejectsUnavailableIDAndRawPredicate(t *testing.T) {
	executor := &fakeNeo4jExecutor{}
	connection := &Neo4jConnection{executor: executor}

	_, err := connection.Insert(context.Background(), newInsertRequest(
		"users", map[string]interface{}{"name": "Ada"}, "uuid", true,
	))
	if !errors.Is(err, ErrInsertIDUnavailable) {
		t.Fatalf("Neo4j missing application primary key must be explicit, got %v", err)
	}

	predicate, predicateErr := NewDB(connection).Table("users").WhereRaw("uuid = ?", "u-1").Predicate()
	if predicateErr != nil {
		t.Fatal(predicateErr)
	}
	_, err = connection.Delete(context.Background(), newDeleteRequest("users", predicate, "uuid", true))
	if !errors.Is(err, ErrUnsafeExpression) {
		t.Fatalf("Neo4j raw predicate must be rejected before executor access, got %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("invalid typed operations must not access Neo4j: %#v", executor.calls)
	}
}

// TestNeo4jConnectionRejectsInvalidRowsAndClosesOnce 验证非法聚合结果不会被吞掉，
// 且执行器关闭错误在重复关闭时保持稳定。
func TestNeo4jConnectionRejectsInvalidRowsAndClosesOnce(t *testing.T) {
	executor := &fakeNeo4jExecutor{singleRecords: []*neo4j.Record{{Values: []interface{}{"invalid"}}}, closeErr: errors.New("close failed")}
	connection := &Neo4jConnection{executor: executor}
	if _, err := connection.Count(context.Background(), newCountRequest("users", newPredicate(), "id")); !errors.Is(err, ErrInvalidAggregateValue) {
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
	if _, err := connection.Select(context.Background(), newSelectRequest("users", "name AS value,email AS value", newPredicate(), "id", "", 0, 0, nil)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("重复投影别名应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatal("重复投影别名不得访问 Neo4j 执行器")
	}

	executor.collectRecords = []*neo4j.Record{{Keys: []string{"name", "name"}, Values: []interface{}{"Ada", "Grace"}}}
	if _, err := connection.Select(context.Background(), newSelectRequest("users", "name,email", newPredicate(), "id", "", 0, 0, nil)); !errors.Is(err, ErrInvalidDatabaseRow) {
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
