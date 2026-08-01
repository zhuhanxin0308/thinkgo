# ORM Model、关系与 NoSQL Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 ThinkPHP 风格 Model Save/Create、写入前主键验证、最终事件数据和可靠关系分组，同时修复 MongoDB ObjectID/projection 与 Neo4j 删除、事务、目标库和属性契约。

**Architecture:** Model 通过预构建 `primaryKeyBinding` 验证并写回真实 `InsertGetId`；事件调度器对每个 callback 深度隔离并使用 typed result 的最终 Data。关系查询保留 raw-key sidecar；Mongo/Neo 在 operation request 的 primary-key/detach/capability 上实现后端特有行为。

**Tech Stack:** Go 1.26.5、reflection、MongoDB Go Driver v2 BSON、Neo4j Go Driver v5 managed transactions、Go race detector。

## Global Constraints

- `Model.Save` 以主键零值选择 Create/Update；Create 必须在写数据库前验证主键字段。
- 主键回填保留真实 ID 类型；Mongo ObjectID 转换仅限声明的主键路径。
- before hook 可按顺序修改即将写入的数据；after hook 看到最终持久化数据；callback 不能共享可变 payload。
- eager-load 分组始终使用数据库 raw key，不使用 Getter 改写后的展示值。
- Neo4j `Delete` 默认严格删除；只有显式 `DetachDelete` 才删除关系。
- Neo4j 操作使用 managed transaction；Connect 必须验证配置的目标 database。
- 不新增配置文件；真实 Mongo/Neo 不可用时保留 live-server 边界。

---

## File Structure

- Create `framework/db/model_primary_key.go`: 主键 metadata、capability 校验和类型安全写回。
- Modify `framework/db/errors.go`: `ErrPartialWrite` 与携带 typed result 的 `PartialWriteError`。
- Create `framework/db/model_event_data.go`: 深拷贝事件调度。
- Modify `framework/db/model.go`, `model_query.go`: Save/Create、最终结果和事件。
- Modify `framework/db/model_relation_keys.go`: raw relation row/sidecar 索引。
- Modify `framework/db/mongo_connection.go`: 主键 ObjectID codec、projection。
- Modify `framework/db/neo4j_connection.go`, `neo4j_executor.go`: strict/detach、managed transaction、计数。
- Modify `framework/db/connector/neo4j.go`: 目标 database 验证。
- Add/modify focused tests and database documentation。

### Task 1: Model Save 与写入前主键 binding

**Files:**
- Create: `framework/db/model_primary_key.go`
- Modify: `framework/db/errors.go`
- Modify: `framework/db/model.go`
- Modify: `framework/db/model_query.go`
- Test: `framework/db/model_hardening_test.go`
- Test: `framework/db/model_struct_update_test.go`

**Interfaces:**
- Produces: `Model.Save(v any) error`、`Model.InsertGetId(data map[string]any) (any, error)`、`primaryKeyBinding`、`ErrPartialWrite`/`PartialWriteError`；Create 使用 `InsertGetId`。

- [ ] **Step 1: 写“非法主键时零写入”和 Save 分支失败测试**

```go
func TestModelCreateValidatesPrimaryKeyBeforeInsert(t *testing.T) {
	connection := &operationRecorder{capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDInteger}}}
	model := NewModel(NewDB(connection), "users")
	bad := &struct { ID bool `thinkgo:"id"`; Name string `thinkgo:"name"` }{Name: "Ada"}
	err := model.Create(bad)
	if !errors.Is(err, ErrInvalidModel) { t.Fatalf("非法主键应拒绝: %v", err) }
	if connection.insertCalls.Load() != 0 { t.Fatal("验证失败后仍执行 Insert") }
}

func TestModelSaveChoosesCreateOrUpdate(t *testing.T) {
	model := NewModel(NewDB(&operationRecorder{insertID: int64(9)}), "users")
	created := &modelUser{Name: "Ada"}
	if err := model.Save(created); err != nil || created.ID != 9 { t.Fatalf("Save create: %#v %v", created, err) }
	updated := &modelUser{ID: 9, Name: "Grace"}
	if err := model.Save(updated); err != nil { t.Fatalf("Save update: %v", err) }
}
```

- [ ] **Step 2: 运行确认 Create 先写后验证/Save 缺失**

Run: `go test ./framework/db -run 'TestModelCreateValidatesPrimaryKeyBeforeInsert|TestModelSaveChoosesCreateOrUpdate' -count=1`

Expected: FAIL，Save 不存在或 insertCalls=1。

- [ ] **Step 3: 实现 primaryKeyBinding 和 Save**

```go
type primaryKeyBinding struct { field reflect.Value; fieldName string }

var ErrPartialWrite = errors.New("database write completed but local result binding failed")

type PartialWriteError struct {
	Result InsertResult
	Cause  error
}

func (e *PartialWriteError) Error() string { return fmt.Sprintf("%v: %v", ErrPartialWrite, e.Cause) }
func (e *PartialWriteError) Unwrap() error { return e.Cause }
func (e *PartialWriteError) Is(target error) bool { return target == ErrPartialWrite }

func preparePrimaryKeyBinding(value reflect.Value, column string, caps DriverCapabilities) (primaryKeyBinding, bool, error) {
	field, fieldType, found := findModelColumn(value, column)
	if !found || !field.CanSet() { return primaryKeyBinding{}, false, fmt.Errorf("%w: 主键 %q 不存在或不可写", ErrInvalidModel, column) }
	if !supportedPrimaryKeyKind(field.Type(), caps.InsertIDKinds) { return primaryKeyBinding{}, false, fmt.Errorf("%w: 主键字段 %s 与驱动 ID 类型不兼容", ErrInvalidModel, fieldType.Name) }
	return primaryKeyBinding{field: field, fieldName: fieldType.Name}, field.IsZero(), nil
}

func (b primaryKeyBinding) Assign(id any) error {
	return assignPrimaryKeyValue(b.field, id) // 精确处理有符号/无符号溢出、string、bson.ObjectID
}

func (m *Model) Save(v any) error {
	value, err := writableModelStruct(v); if err != nil { return err }
	binding, zero, err := m.preparePrimaryKey(value); if err != nil { return err }
	if zero { return m.createWithBinding(v, binding) }
	return m.Update(v)
}
```

`Create` 在任何 event/Insert 前调用 `preparePrimaryKeyBinding`；零主键从 data 删除，调用内部 typed insert 并取得完整 `InsertResult`，随后 `Assign`。若 capability 为 dynamic 且运行时仍不兼容，返回 `&PartialWriteError{Result: result, Cause: err}`；SQL 可用事务路径在 Assign 失败时回滚。为 map 风格模型补齐 `Model.Insert`（返回条数）和 `Model.InsertGetId`（返回真实 ID）并直接委托同名 Query API，不能把条数再解释为主键。

- [ ] **Step 4: 运行 Model CRUD/race 回归**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestModelCreate|TestModelSave|TestModelStruct|TestModelUpdate' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/errors.go framework/db/model_primary_key.go framework/db/model.go framework/db/model_query.go framework/db/model_hardening_test.go framework/db/model_struct_update_test.go
git commit -m "fix(db): validate model primary keys before writes"
```

### Task 2: 深度隔离 hook 并发送最终持久化数据

**Files:**
- Create: `framework/db/model_event_data.go`
- Modify: `framework/db/model.go`
- Modify: `framework/db/model_query.go`
- Modify: `framework/db/query_execute.go`
- Test: `framework/db/model_event_test.go`

**Interfaces:**
- Produces: `dispatchBeforeEvent`、`dispatchAfterEvent`；typed results 的 `Data` 是最终写入快照。

- [ ] **Step 1: 写 nested payload 隔离和最终字段失败测试**

```go
func TestModelEventsOwnDeepPayloadsAndSeeFinalData(t *testing.T) {
	var first map[string]any
	model.On(ModelBeforeInsert, func(data map[string]any) bool { first = data; data["meta"].(map[string]any)["role"] = "writer"; return true })
	model.On(ModelBeforeInsert, func(data map[string]any) bool { data["name"] = "Grace"; return true })
	var after map[string]any
	model.On(ModelAfterInsert, func(data map[string]any) bool { after = data; return true })
	input := map[string]any{"name": "Ada", "meta": map[string]any{"role": "reader"}}
	if _, err := model.InsertGetId(input); err != nil { t.Fatal(err) }
	if first["name"] != "Ada" { t.Fatalf("后续 callback 污染历史 payload: %#v", first) }
	if after["id"] == nil || after["create_time"] == nil || after["name"] != "Grace" { t.Fatalf("after 非最终数据: %#v", after) }
	if input["meta"].(map[string]any)["role"] != "reader" { t.Fatalf("输入被 hook 污染: %#v", input) }
}
```

- [ ] **Step 2: 运行确认浅拷贝/最终字段缺失**

Run: `go test ./framework/db -run 'TestModelEventsOwnDeepPayloadsAndSeeFinalData' -count=1`

Expected: FAIL，历史 payload 被改或 after 缺少时间戳/ID。

- [ ] **Step 3: 实现顺序 before 与只读 after 快照**

```go
func (m *Model) dispatchBeforeEvent(kind ModelEventType, data map[string]any) (map[string]any, bool) {
	current := cloneDatabaseMap(data)
	for _, callback := range m.eventSnapshot(kind) {
		payload := cloneDatabaseMap(current)
		if !callback(payload) { return nil, false }
		current = payload
	}
	return current, true
}

func (m *Model) dispatchAfterEvent(kind ModelEventType, data map[string]any) {
	for _, callback := range m.eventSnapshot(kind) { _ = callback(cloneDatabaseMap(data)) }
}
```

Query `insertResult/updateResult` 在应用自动时间戳后设置 `result.Data=cloneDatabaseMap(workingData)`；ModelQuery after event 使用 result.Data，并在 insert 后加入真实主键。Delete/soft delete after payload 包含实际 delete_time 和 typed delete result 摘要。

- [ ] **Step 4: 运行所有事件与 setter 测试**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestModel.*Event|TestModelSetter' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/model_event_data.go framework/db/model.go framework/db/model_query.go framework/db/query_execute.go framework/db/model_event_test.go
git commit -m "fix(db): isolate model event payloads"
```

### Task 3: eager-load 使用 raw relation key sidecar

**Files:**
- Modify: `framework/db/model_relation_keys.go`
- Modify: `framework/db/model_query.go`
- Test: `framework/db/model_relation_test.go`
- Test: `framework/db/model_hardening_test.go`

**Interfaces:**
- Produces: `relationRow{data, rawKeys}`、raw-key collect/group/index helpers。

- [ ] **Step 1: 写 Getter 改写四类关系键仍可关联的失败测试**

```go
func TestEagerRelationsGroupByRawKeysBeforeGetters(t *testing.T) {
	users, profiles, managers, roles := getterRelationFixture(t)
	_ = profiles.Getter("user_id", func(any, map[string]any) any { return "display-user" })
	_ = managers.Getter("id", func(any, map[string]any) any { return "display-manager" })
	_ = roles.Getter("id", func(any, map[string]any) any { return "display-role" })
	rows, err := users.With("profile", "manager", "roles").Select()
	if err != nil { t.Fatal(err) }
	if rows[0]["profile"] == nil || rows[0]["manager"] == nil || len(rows[0]["roles"].([]map[string]any)) != 2 { t.Fatalf("raw key 关联失败: %#v", rows[0]) }
}
```

- [ ] **Step 2: 运行确认 related Select 先应用 Getter 导致空关系**

Run: `go test ./framework/db -run 'TestEagerRelationsGroupByRawKeysBeforeGetters' -count=1`

Expected: FAIL，至少一个关系为空。

- [ ] **Step 3: 保留 raw keys 后再应用展示 Getter**

```go
type relationRow struct { data map[string]any; rawKeys map[string]any }

func makeRelationRows(rows []map[string]any, keyFields []string, model *Model) ([]relationRow, error) {
	result := make([]relationRow, len(rows))
	for i, row := range rows {
		raw := make(map[string]any, len(keyFields))
		for _, field := range keyFields {
			value, ok := row[field]; if !ok { return nil, fmt.Errorf("%w: 第 %d 行缺少 %s", ErrInvalidRelation, i, field) }
			raw[field] = cloneDatabaseValue(value)
		}
		result[i] = relationRow{data: model.applyGetters(cloneDatabaseMap(row)), rawKeys: raw}
	}
	return result, nil
}
```

关系内部查询使用 `selectRelationRows(keyFields...)`，group/index 从 `rawKeys[field]` 生成 canonical key，把已经应用 getter 的 `data` 放进结果。父行在 applyGetters 前直接快照 local/foreign key；has-one、has-many、belongs-to、belongs-to-many 全部走同一 sidecar helper。

- [ ] **Step 4: 运行关系全套与 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'Test.*Relation|TestEagerRelations' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/model_relation_keys.go framework/db/model_query.go framework/db/model_relation_test.go framework/db/model_hardening_test.go
git commit -m "fix(db): preserve raw eager relation keys"
```

### Task 4: MongoDB 主键 ObjectID 往返与 `_id` 投影

**Files:**
- Modify: `framework/db/operation.go`
- Modify: `framework/db/query_execute.go`
- Modify: `framework/db/mongo_connection.go`
- Test: `framework/db/mongo_filter_test.go`
- Test: `framework/db/mongo_connection_io_test.go`

**Interfaces:**
- Produces: Select/Update/Delete request 的 `PrimaryKey()`；Mongo 仅在该字段做 string↔ObjectID。

- [ ] **Step 1: 写 ObjectID 往返、普通字符串不转换和 projection 失败测试**

```go
func TestMongoPrimaryKeyObjectIDRoundTripOnly(t *testing.T) {
	id := bson.NewObjectID()
	request := newSelectRequestWithPrimaryKey("users", "name", predicateEqual("_id", id.Hex()), "_id")
	filter, options, err := connection.mongoFindParts(request)
	if err != nil { t.Fatal(err) }
	if filter["_id"] != id { t.Fatalf("_id 未转回 ObjectID: %#v", filter) }
	if options.Projection["_id"] != 0 { t.Fatalf("显式 name 投影应排除 _id: %#v", options.Projection) }
	plain := predicateEqual("external_code", id.Hex())
	if got := connection.mongoFilter(plain, "_id")["external_code"]; got != id.Hex() { t.Fatalf("普通字段被误转: %#v", got) }
}

func TestMongoNormalizationOnlyStringifiesPrimaryKey(t *testing.T) {
	id, nested := bson.NewObjectID(), bson.NewObjectID()
	row := normalizeMongoDocument(bson.M{"_id": id, "owner_id": nested}, "_id")
	if row["_id"] != id.Hex() || row["owner_id"] != nested { t.Fatalf("ObjectID 范围错误: %#v", row) }
}
```

- [ ] **Step 2: 运行确认当前所有 ObjectID 递归字符串化且 `_id` 附带**

Run: `go test ./framework/db -run 'TestMongoPrimaryKeyObjectIDRoundTripOnly|TestMongoNormalizationOnlyStringifiesPrimaryKey' -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现主键限定 codec 与投影规则**

```go
func coerceMongoPrimaryKey(value any) (any, error) {
	text, ok := value.(string); if !ok { return value, nil }
	id, err := bson.ObjectIDFromHex(text); if err != nil { return nil, fmt.Errorf("%w: MongoDB 主键不是合法 ObjectID", ErrInvalidQuery) }
	return id, nil
}

func normalizeMongoDocument(document bson.M, primaryKey string) map[string]any {
	result := cloneDatabaseMap(document)
	if id, ok := result[primaryKey].(bson.ObjectID); ok { result[primaryKey] = id.Hex() }
	return result
}
```

Filter walker 只在字段等于 `request.PrimaryKey()` 时转换 comparison/IN/BETWEEN 值；普通字段保留。显式 projection 未列 `_id` 时加入 `_id:0`，显式 `_id` 或 `*` 保留。

- [ ] **Step 4: 运行 Mongo 单元、mock I/O 和 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestMongo' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/operation.go framework/db/query_execute.go framework/db/mongo_connection.go framework/db/mongo_filter_test.go framework/db/mongo_connection_io_test.go
git commit -m "fix(db): round trip Mongo primary ObjectIDs"
```

### Task 5: Neo4j 严格删除、显式 DetachDelete 和属性键一致性

**Files:**
- Modify: `framework/db/query_execute.go`
- Modify: `framework/db/neo4j_connection.go`
- Modify: `framework/db/neo4j_executor.go`
- Test: `framework/db/neo4j_connection_test.go`

**Interfaces:**
- Produces: `Query.DetachDelete()`, `Query.DetachDeleteResult()`；Neo `DeleteResult` 的 node/relationship counters。

- [ ] **Step 1: 写严格/DETACH 查询和点分属性拒绝测试**

```go
func TestNeoDeleteModesAreExplicit(t *testing.T) {
	strict, err := connection.Delete(context.Background(), deleteRequest(false))
	if err != nil || !strings.Contains(executor.lastCypher(), " DELETE n") || strings.Contains(executor.lastCypher(), "DETACH") { t.Fatalf("strict=%#v %v %s", strict, err, executor.lastCypher()) }
	detached, err := connection.Delete(context.Background(), deleteRequest(true))
	if err != nil || !strings.Contains(executor.lastCypher(), "DETACH DELETE n") || !detached.RelatedDeletedKnown { t.Fatalf("detach=%#v %v", detached, err) }
}

func TestNeoRejectsDottedPropertyOnEveryWrite(t *testing.T) {
	for _, execute := range []func() error{
		func() error { _, err := connection.Insert(context.Background(), insertRequest(map[string]any{"profile.name": "Ada"})); return err },
		func() error { _, err := connection.Update(context.Background(), updateRequest(map[string]any{"profile.name": "Ada"})); return err },
	} { if err := execute(); !errors.Is(err, ErrInvalidQuery) { t.Fatalf("点分属性未拒绝: %v", err) } }
}
```

- [ ] **Step 2: 运行确认默认固定 DETACH 且写路径放行点号**

Run: `go test ./framework/db -run 'TestNeoDeleteModesAreExplicit|TestNeoRejectsDottedPropertyOnEveryWrite' -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现 delete flag、summary counters 和统一属性验证**

```go
func validateNeo4jProperties(data map[string]any) error {
	for key := range data { if _, err := cypherIdentifier(key); err != nil { return err } }
	return nil
}

func (q *Query) DetachDeleteResult() (DeleteResult, error) { return q.deleteResult(true) }
func (q *Query) DetachDelete() (int64, error) {
	result, err := q.DetachDeleteResult(); if err != nil { return 0, err }
	return result.Deleted, nil
}
```

Neo Delete 根据 request flag 选择 `DELETE n` 或 `DETACH DELETE n`。Executor Consume summary 返回 `DeleteResult{Deleted: NodesDeleted, RelatedDeleted: RelationshipsDeleted, RelatedDeletedKnown:true}`；所有 create/update property map 先调用同一个 `validateNeo4jProperties`。

- [ ] **Step 4: 运行 Neo4j 全套与 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestNeo' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/query_execute.go framework/db/neo4j_connection.go framework/db/neo4j_executor.go framework/db/neo4j_connection_test.go
git commit -m "fix(db): make Neo4j detach deletion explicit"
```

### Task 6: Neo4j managed transactions 与目标 database 验证

**Files:**
- Modify: `framework/db/neo4j_executor.go`
- Modify: `framework/db/connector/neo4j.go`
- Test: `framework/db/neo4j_connection_test.go`
- Test: `framework/db/connector/neo4j_test.go`

**Interfaces:**
- Produces: 所有 Neo operation 经 `Session.ExecuteRead/ExecuteWrite`；`verifyNeo4jTargetDatabase`。

- [ ] **Step 1: 写 managed transaction 与目标库失败测试**

```go
func TestNeoExecutorUsesManagedTransactions(t *testing.T) {
	session := &fakeManagedSession{}
	executor := newExecutorWithSession(session)
	if _, err := executor.Single(context.Background(), neo4j.AccessModeWrite, "CREATE (n)", nil); err != nil { t.Fatal(err) }
	if session.executeWriteCalls != 1 || session.runCalls != 1 { t.Fatalf("managed calls=%#v", session) }
}

func TestNeoConnectVerifiesConfiguredDatabase(t *testing.T) {
	driver := &fakeNeoDriver{databaseError: errors.New("database not found")}
	_, err := connectNeoWithDriver(validNeoConfig("tenant_a"), driver)
	if err == nil || driver.lastDatabase != "tenant_a" { t.Fatalf("目标库未验证: db=%s err=%v", driver.lastDatabase, err) }
	if driver.closeCalls != 1 { t.Fatalf("失败后 driver 未关闭: %d", driver.closeCalls) }
}
```

- [ ] **Step 2: 运行确认仍使用 Session.Run/只 VerifyConnectivity**

Run: `go test ./framework/db ./framework/db/connector -run 'TestNeoExecutorUsesManagedTransactions|TestNeoConnectVerifiesConfiguredDatabase' -count=1`

Expected: FAIL。

- [ ] **Step 3: 用 ExecuteRead/ExecuteWrite 包装工作单元**

```go
func runNeoManaged(ctx context.Context, session neo4j.SessionWithContext, mode neo4j.AccessMode, work neo4j.ManagedTransactionWork) (any, error) {
	if mode == neo4j.AccessModeRead { return session.ExecuteRead(ctx, work) }
	return session.ExecuteWrite(ctx, work)
}

func (executor *neo4jDriverExecutor) Single(ctx context.Context, mode neo4j.AccessMode, cypher string, params map[string]any) (*neo4j.Record, error) {
	return withNeoSession(executor, ctx, mode, func(session neo4j.SessionWithContext) (*neo4j.Record, error) {
		value, err := runNeoManaged(ctx, session, mode, func(tx neo4j.ManagedTransaction) (any, error) {
			result, err := tx.Run(ctx, cypher, cloneDatabaseMap(params)); if err != nil { return nil, err }
			return result.Single(ctx)
		})
		if err != nil { return nil, err }
		return value.(*neo4j.Record), nil
	})
}
```

Collect/Execute 使用同一 helper。Connector 在 VerifyConnectivity 后，以 `SessionConfig{DatabaseName: validated.Database, AccessMode: Read}` 开 session，通过 ExecuteRead 执行 `RETURN 1 AS thinkgo_probe` 并消费 Single；失败时关闭 session 和 driver，使用 `errors.Join`。

- [ ] **Step 4: 运行 executor/connector/race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db ./framework/db/connector -run 'TestNeo' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/neo4j_executor.go framework/db/connector/neo4j.go framework/db/neo4j_connection_test.go framework/db/connector/neo4j_test.go
git commit -m "fix(db): use managed Neo4j transactions"
```

### Task 7: Model/NoSQL 文档、审计状态与验证

**Files:**
- Modify: `docs/数据库/模型.md`
- Modify: `docs/数据库/MongoDB与Neo4j.md`
- Modify: `docs/数据库/ORM源码审计-2026-07-13.md`

**Interfaces:**
- Produces: M-01/M-02/M-03/NQ-02/NQ-03/NQ-04/NQ-05/NQ-06/NQ-07 修复状态。

- [ ] **Step 1: 运行 Model/Mongo/Neo 定向 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db ./framework/db/connector -run 'TestModelCreate|TestModelSave|TestModelEventsOwn|TestEagerRelations|TestMongo|TestNeo' -count=1`

Expected: PASS。

- [ ] **Step 2: 运行真实服务集成边界检查**

Run: `go test -tags integration ./framework/db -run 'TestLiveMongo|TestLiveNeo' -count=1 -v`

Expected: integration test 文件已由性能/最终计划 Task 4 建立后，服务与凭据存在时 PASS；未配置时测试输出明确 SKIP。若尚未执行性能/最终计划 Task 4，本步骤记录 `not run: integration test scheduled in performance plan Task 4`，不能创建假 PASS。

- [ ] **Step 3: 更新文档与九项审计状态**

模型文档增加 Save/Create 决策、支持的主键类型、before/after 数据时点和 raw relation key；NoSQL 文档增加 `_id` projection、主键 ObjectID 对称转换、Neo strict/DetachDelete、managed retry 与目标库探测。运行 `git log --oneline -6`，将直接对应 commit/test 写入九项 finding。

- [ ] **Step 4: 搜索旧语义和校验空白**

Run: `rg -n '固定 DETACH|Insert.*最后插入 ID|所有 ObjectID|after_insert' docs/数据库 -g '*.md'; git diff --check`

Expected: 不再宣称 Neo 默认 DETACH 或 NoSQL Insert 返回数量即 ID；diff check PASS。

- [ ] **Step 5: 提交**

```powershell
git add docs/数据库/模型.md docs/数据库/MongoDB与Neo4j.md docs/数据库/ORM源码审计-2026-07-13.md
git commit -m "docs: update ORM model and NoSQL contracts"
```
