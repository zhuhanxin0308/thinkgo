# ORM API、结果契约与查询安全 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立 ThinkPHP 风格写入 API、结构化跨驱动结果、不可伪造的条件来源和可并发派生的 Query，并删除旧的含糊/不安全低层接口。

**Architecture:** Query 持有不可变 `Predicate` 与投影状态，执行时构造只读 operation request；Connection 统一接收 `context.Context + request` 并返回 typed result。迁移期间只短暂保留 `OperationConnection` 桥接接口，阶段末删除旧 `where []string` Connection 契约。

**Tech Stack:** Go 1.26.5、`database/sql`、MongoDB Go Driver v2、Neo4j Go Driver v5、CGO SQLite、Go race detector。

## Global Constraints

- 采用一步到位的破坏性升级，不保留旧 `Insert` 返回主键的兼容开关。
- 公共方法名必须使用 `InsertGetId`，不能改成 `InsertGetID`。
- `Insert` 返回插入数量；`InsertGetId` 返回真实主键；`UpdateResult` 区分 matched/modified。
- 普通条件 API 与显式 Raw API 必须保留 provenance；低层 Connection 不接收 `where []string`。
- 所有 Query 链式方法 copy-on-write；基础 Query 可被多个 goroutine 安全派生。
- 不增加配置文件，不修改主工作区 `.env.example`。
- 每个行为修改必须先有失败测试，再写生产代码。

---

## File Structure

- Create `framework/db/operation.go`: typed results、capability、只读 operation requests。
- Create `framework/db/predicate.go`: 条件 provenance、深拷贝和 SQL/NoSQL 只读快照。
- Create `framework/db/operation_contract_test.go`: typed result 与 request 不可变性契约。
- Create `framework/db/query_immutable_test.go`: Query/ModelQuery 分支隔离与 race 回归。
- Modify `framework/db/connection.go`: 最终 Connection/RawQueryable context 化接口。
- Modify `framework/db/conditions.go`: 安全条件编译结果进入 `Predicate`，Raw 单独标记。
- Modify `framework/db/query.go`: copy-on-write 链式状态、typed predicate/aggregate。
- Modify `framework/db/query_execute.go`: ThinkPHP 写入 API 和 typed result 投影。
- Modify `framework/db/query_sql.go`: request 构造和 typed aggregate SQL。
- Modify `framework/db/query_aggregate.go`: 不再把聚合表达式伪装成字段字符串。
- Modify `framework/db/sql_connection.go`: request 执行与 SQL typed result。
- Modify `framework/db/mongo_connection.go`: operation request 和真实 Insert/Update result。
- Modify `framework/db/neo4j_connection.go`: operation request 和不伪造 ID 的 result。
- Modify `framework/db/model.go`, `framework/db/model_query.go`: 适配不可变 Query 与新写入返回值；Model 细节在 Model/NoSQL 计划完成。
- Modify all `framework/db/**/*_test.go` Connection doubles: 迁移为最终接口。

### Task 1: 定义 typed result、capability 和稳定错误

**Files:**
- Create: `framework/db/operation.go`
- Create: `framework/db/operation_contract_test.go`
- Modify: `framework/db/errors.go`

**Interfaces:**
- Produces: `InsertResult`, `UpdateResult`, `DeleteResult`, `DriverCapabilities`, `ErrInsertIDUnavailable`, `ErrUnsafeExpression`, `ErrInvalidOperationResult`。

- [ ] **Step 1: 写失败测试固定结果投影和非法计数**

```go
func TestOperationResultsExposeStableCounts(t *testing.T) {
	update := UpdateResult{Affected: 3, Matched: 3, Modified: 2, MatchedKnown: true, ModifiedKnown: true}
	if got := update.Count(); got != 2 {
		t.Fatalf("Update 简化数量应优先 Modified，实际 %d", got)
	}
	legacySQL := UpdateResult{Affected: 4}
	if got := legacySQL.Count(); got != 4 {
		t.Fatalf("未知 matched/modified 时应返回驱动 affected，实际 %d", got)
	}
	if err := (InsertResult{Affected: -1}).Validate(); !errors.Is(err, ErrInvalidOperationResult) {
		t.Fatalf("负计数应拒绝，实际 %v", err)
	}
}

func TestInsertResultRequiresRealID(t *testing.T) {
	_, err := (InsertResult{Affected: 1}).InsertedID()
	if !errors.Is(err, ErrInsertIDUnavailable) {
		t.Fatalf("缺少真实 ID 应返回 ErrInsertIDUnavailable，实际 %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认缺少类型而失败**

Run: `go test ./framework/db -run 'TestOperationResults' -count=1`

Expected: FAIL，编译错误包含 `undefined: UpdateResult`。

- [ ] **Step 3: 实现结果类型和能力描述**

```go
type InsertIDKind string

const (
	InsertIDNone     InsertIDKind = "none"
	InsertIDInteger  InsertIDKind = "integer"
	InsertIDString   InsertIDKind = "string"
	InsertIDObjectID InsertIDKind = "object_id"
	InsertIDDynamic  InsertIDKind = "dynamic"
)

type InsertResult struct {
	Affected int64
	ID       any
	IDKnown  bool
	Data     map[string]any
}

func (r InsertResult) Validate() error {
	if r.Affected < 0 || r.IDKnown && r.ID == nil {
		return ErrInvalidOperationResult
	}
	return nil
}

func (r InsertResult) InsertedID() (any, error) {
	if err := r.Validate(); err != nil { return nil, err }
	if !r.IDKnown { return nil, ErrInsertIDUnavailable }
	return r.ID, nil
}

type UpdateResult struct {
	Affected      int64
	Matched       int64
	Modified      int64
	MatchedKnown  bool
	ModifiedKnown bool
	Data          map[string]any
}

func (r UpdateResult) Count() int64 {
	if r.ModifiedKnown { return r.Modified }
	return r.Affected
}

type DeleteResult struct {
	Deleted             int64
	RelatedDeleted      int64
	RelatedDeletedKnown bool
}

type DriverCapabilities struct {
	InsertIDKinds       []InsertIDKind
	MatchedCountKnown   bool
	ModifiedCountKnown  bool
}

type CapabilityProvider interface { Capabilities() DriverCapabilities }
```

在 `errors.go` 增加四个稳定 sentinel，并让三个 `Validate` 方法拒绝负数、Known 与值矛盾及计数溢出。

- [ ] **Step 4: 运行结果契约测试**

Run: `go test ./framework/db -run 'TestOperationResults|TestInsertResult' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/operation.go framework/db/operation_contract_test.go framework/db/errors.go
git commit -m "feat(db): add typed operation results"
```

### Task 2: 引入不可伪造 Predicate 与只读 operation request

**Files:**
- Create: `framework/db/predicate.go`
- Modify: `framework/db/operation.go`
- Modify: `framework/db/conditions.go`
- Test: `framework/db/operation_contract_test.go`

**Interfaces:**
- Consumes: Task 1 result types。
- Produces: `Predicate`, `PredicateClause`, `SelectRequest`, `InsertRequest`, `UpdateRequest`, `DeleteRequest`, `CountRequest` 及只读 accessor；所有读/改/删 request 都携带只读 `PrimaryKey()`，供 MongoDB 主键 codec 和各驱动结果契约使用。

- [ ] **Step 1: 写 provenance 与深拷贝失败测试**

```go
func TestPredicateSeparatesValidatedAndRawClauses(t *testing.T) {
	p := newPredicate().appendValidated("status = ?", []any{"open"}).appendRaw("score > ?", []any{10})
	clauses := p.Clauses()
	if clauses[0].UnsafeRaw || !clauses[1].UnsafeRaw { t.Fatalf("provenance 丢失: %#v", clauses) }
	clauses[0].Args[0] = "mutated"
	if got := p.Clauses()[0].Args[0]; got != "open" { t.Fatalf("Predicate 被外部修改: %v", got) }
}

func TestOperationRequestClonesMutableInputs(t *testing.T) {
	data := map[string]any{"meta": map[string]any{"role": "admin"}}
	r := newInsertRequest("users", data, "id", true)
	data["meta"].(map[string]any)["role"] = "changed"
	if got := r.Data()["meta"].(map[string]any)["role"]; got != "admin" { t.Fatalf("request 未深拷贝: %v", got) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./framework/db -run 'TestPredicate|TestOperationRequest' -count=1`

Expected: FAIL，缺少 `Predicate`/request 构造函数。

- [ ] **Step 3: 实现 private storage + defensive accessors**

```go
type PredicateClause struct {
	Connector string
	SQL       string
	Args      []any
	UnsafeRaw bool
}

type Predicate struct { clauses []PredicateClause }

func newPredicate() Predicate { return Predicate{} }
func (p Predicate) appendValidated(sql string, args []any) Predicate { return p.append(sql, args, false) }
func (p Predicate) appendRaw(sql string, args []any) Predicate { return p.append(sql, args, true) }
func (p Predicate) append(sql string, args []any, raw bool) Predicate {
	next := Predicate{clauses: clonePredicateClauses(p.clauses)}
	next.clauses = append(next.clauses, PredicateClause{Connector: "AND", SQL: sql, Args: cloneDatabaseValues(args), UnsafeRaw: raw})
	return next
}
func (p Predicate) Clauses() []PredicateClause { return clonePredicateClauses(p.clauses) }
func (p Predicate) Empty() bool { return len(p.clauses) == 0 }

type InsertRequest struct { table string; data map[string]any; primaryKey string; wantID bool }
func newInsertRequest(table string, data map[string]any, primaryKey string, wantID bool) InsertRequest {
	return InsertRequest{table: table, data: cloneDatabaseMap(data), primaryKey: primaryKey, wantID: wantID}
}
func (r InsertRequest) Table() string { return r.table }
func (r InsertRequest) Data() map[string]any { return cloneDatabaseMap(r.data) }
func (r InsertRequest) PrimaryKey() string { return r.primaryKey }
func (r InsertRequest) WantsID() bool { return r.wantID }
```

以相同 private-field/accessor 规则完整实现 Select/Update/Delete/Count request；四者都包含 `primaryKey string` 和 `PrimaryKey() string`，Select 另含 fields、predicate、order、limit、offset、aggregate、lock，Update/Delete/Count 包含 predicate，Delete 额外包含 detach 标志。`newSelectRequest`、`newUpdateRequest`、`newDeleteRequest`、`newCountRequest` 必须由 Query 传入 `q.primaryKey`，默认值为空时使用当前 Query 的 `insertPrimaryKey`。`cloneDatabaseValue` 必须递归复制 map、slice、array、pointer-backed 值和 `[]byte`。

- [ ] **Step 4: 运行 request 契约和现有条件测试**

Run: `go test ./framework/db -run 'TestPredicate|TestOperationRequest|TestConditionGroup' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/predicate.go framework/db/operation.go framework/db/conditions.go framework/db/operation_contract_test.go
git commit -m "feat(db): add typed operation requests"
```

### Task 3: Query 与 ModelQuery 全链 copy-on-write

**Files:**
- Create: `framework/db/query_immutable_test.go`
- Modify: `framework/db/query.go`
- Modify: `framework/db/query_sql.go`
- Modify: `framework/db/model.go`
- Modify: `framework/db/model_query.go`
- Modify: tests registering `ModelSearcherFunc`

**Interfaces:**
- Produces: 所有链式方法返回独立 Query；`ModelSearcherFunc` 返回 `*Query`。

- [ ] **Step 1: 写分支隔离与并发失败测试**

```go
func TestQueryBranchesAreImmutable(t *testing.T) {
	base := NewDB(&operationRecorder{}).Table("users")
	active := base.WhereField("status", "=", 1).Order("id")
	deleted := base.WhereField("deleted", "=", 1).Limit(5)
	if !base.predicate.Empty() || base.order != "" || base.limit != 0 { t.Fatalf("基础 Query 被污染: %#v", base) }
	if len(active.predicate.Clauses()) != 1 || len(deleted.predicate.Clauses()) != 1 { t.Fatal("分支条件不完整") }
}

func TestQueryConcurrentDerivation(t *testing.T) {
	base := NewDB(&operationRecorder{}).Table("users")
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(id int) { defer wg.Done(); _ = base.WhereField("id", "=", id).Limit(1) }(i)
	}
	wg.Wait()
}
```

- [ ] **Step 2: 用 race 运行并确认失败**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestQueryBranches|TestQueryConcurrent' -count=1`

Expected: FAIL，基础状态被修改或 race detector 报告 Query 字段竞争。

- [ ] **Step 3: 让每个链式入口先克隆再修改**

```go
func (q *Query) next() *Query {
	if q == nil { return nil }
	return q.clone()
}

func (q *Query) WhereField(field, op string, value any) *Query {
	next := q.next()
	if next == nil { return nil }
	clause, err := compileFieldPredicate(field, op)
	if err != nil { return next.setError(err) }
	next.predicate = next.predicate.appendValidated(clause, []any{cloneDatabaseValue(value)})
	return next
}

func (q *Query) WhereRaw(rawSQL string, args ...any) *Query {
	next := q.next()
	if next == nil { return nil }
	if err := validateRawClause(rawSQL, len(args), "where"); err != nil { return next.setError(err) }
	next.predicate = next.predicate.appendRaw(rawSQL, args)
	return next
}
```

按同一规则修改 `WithContext/Where*/Limit/Offset/Page/Order/Field/Group/Having*/Distinct/Join*/Inc/Dec/Lock`。`clone()` 深拷贝 predicate、nested map/slice、joins、having、set expressions。ModelQuery 方法先 `next := mq.clone()`，再令 `next.query = next.query.Where...`。

把搜索器签名改为：

```go
type ModelSearcherFunc func(query *Query, value any, data map[string]any) *Query
```

`WithSearch` 必须接收返回值：`mq.query = searcher(mq.query, value, cloneDatabaseMap(searchData))`。

- [ ] **Step 4: 运行 Query/ModelQuery/race 回归**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestQueryBranches|TestQueryConcurrent|TestModelQuery|TestWithSearch' -count=1`

Expected: PASS，无 race。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/query.go framework/db/query_sql.go framework/db/model.go framework/db/model_query.go framework/db/query_immutable_test.go framework/db/*_test.go
git commit -m "fix(db): make query chains copy on write"
```

### Task 4: SQLConnection 使用 request 并支持 typed aggregate/result

**Files:**
- Modify: `framework/db/connection.go`
- Modify: `framework/db/sql_connection.go`
- Modify: `framework/db/query_execute.go`
- Modify: `framework/db/query_aggregate.go`
- Modify: `framework/db/query_sql.go`
- Modify: `framework/db/builder.go`
- Test: `framework/db/sql_connection_hardening_test.go`
- Test: `framework/db/connector/sqlite_orm_integration_test.go`

**Interfaces:**
- Produces temporary: `OperationConnection` with `SelectOperation`, `InsertOperation`, `UpdateOperation`, `DeleteOperation`, `CountOperation`。

- [ ] **Step 1: 写低层来源边界、ID/count 和聚合失败测试**

```go
func TestSQLConnectionExecutesOnlyTypedPredicate(t *testing.T) {
	request := newUpdateRequest("users", map[string]any{"name": "Ada"}, newPredicate().appendValidated("id = ?", []any{7}))
	result, err := connection.UpdateOperation(context.Background(), request)
	if err != nil || result.Affected != 1 { t.Fatalf("typed update 失败: %#v %v", result, err) }
}

func TestSQLiteInsertAndInsertGetIdAreDistinct(t *testing.T) {
	count, err := database.Table("users").Insert(map[string]any{"name": "Ada"})
	if err != nil || count != 1 { t.Fatalf("Insert 应返回 1: %d %v", count, err) }
	id, err := database.Table("users").InsertGetId(map[string]any{"name": "Lin"})
	if err != nil || id.(int64) < 1 { t.Fatalf("InsertGetId 应返回真实 ID: %#v %v", id, err) }
}

func TestSQLiteSimplePathAggregates(t *testing.T) {
	for _, fn := range []func(*Query) (float64, error){
		func(q *Query) (float64, error) { return q.Sum("amount") },
		func(q *Query) (float64, error) { return q.Avg("amount") },
		func(q *Query) (float64, error) { return q.Min("amount") },
		func(q *Query) (float64, error) { return q.Max("amount") },
	} { if _, err := fn(database.Table("orders")); err != nil { t.Fatal(err) } }
}
```

- [ ] **Step 2: 运行测试确认旧接口/聚合路径失败**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db ./framework/db/connector -run 'TestSQLConnectionExecutesOnly|TestSQLiteInsertAndInsertGetId|TestSQLiteSimplePathAggregates' -count=1`

Expected: FAIL，缺少 Operation 方法，聚合命中 unsafe identifier。

- [ ] **Step 3: 实现 SQL operation 执行路径**

```go
type OperationConnection interface {
	SelectOperation(context.Context, SelectRequest) ([]map[string]any, error)
	InsertOperation(context.Context, InsertRequest) (InsertResult, error)
	UpdateOperation(context.Context, UpdateRequest) (UpdateResult, error)
	DeleteOperation(context.Context, DeleteRequest) (DeleteResult, error)
	CountOperation(context.Context, CountRequest) (int64, error)
}

func (c *SQLConnection) InsertOperation(ctx context.Context, request InsertRequest) (InsertResult, error) {
	data := request.Data()
	if err := validateWriteArguments(request.Table(), data); err != nil { return InsertResult{}, err }
	if request.WantsID() { return c.insertWithID(ctx, request) }
	query, args := c.Builder.Insert(request.Table(), data)
	result, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), args...)
	if err != nil { return InsertResult{}, err }
	affected, err := result.RowsAffected()
	return InsertResult{Affected: affected, Data: data}, err
}

func (c *SQLConnection) UpdateOperation(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	where, args, err := request.Predicate().compileSQL(c.Builder)
	if err != nil { return UpdateResult{}, err }
	query, values := c.Builder.Update(request.Table(), request.Data(), where)
	result, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), append(values, args...)...)
	if err != nil { return UpdateResult{}, err }
	affected, err := result.RowsAffected()
	return UpdateResult{Affected: affected, Data: request.Data()}, err
}
```

Select/Delete/Count 使用相同 `Predicate.compileSQL`；所有 request 在执行前验证。定义 `AggregateExpression{Function, Field, Alias}`，函数只允许 SUM/AVG/MIN/MAX，字段使用 `Builder.QuoteIdentifier`，别名固定 `tp_aggregate`，不再写入 `q.fields` 字符串。Query 执行优先断言 `OperationConnection`。

- [ ] **Step 4: 运行 SQL、SQLite 与 builder 回归**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db/... -run 'TestSQLConnectionExecutesOnly|TestSQLiteInsertAndInsertGetId|TestSQLiteSimplePathAggregates|TestWriteBuilders' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/connection.go framework/db/sql_connection.go framework/db/query_execute.go framework/db/query_aggregate.go framework/db/query_sql.go framework/db/builder.go framework/db/*_test.go framework/db/connector/*_test.go
git commit -m "feat(db): execute typed SQL operations"
```

### Task 5: MongoDB/Neo4j 返回真实 typed result

**Files:**
- Modify: `framework/db/mongo_connection.go`
- Modify: `framework/db/neo4j_connection.go`
- Test: `framework/db/mongo_connection_io_test.go`
- Test: `framework/db/neo4j_connection_test.go`

**Interfaces:**
- Consumes: `OperationConnection`, operation requests/results。
- Produces: Mongo InsertedID；Neo4j 无主键时明确 `ErrInsertIDUnavailable`；两端 Update capability 正确。

- [ ] **Step 1: 写 Mongo/Neo 返回语义失败测试**

```go
func TestMongoOperationResultsPreserveIDAndCounts(t *testing.T) {
	id := bson.NewObjectID()
	connection := mongoConnectionRespondingWith(insertOneResponse(id), updateResponse(3, 2))
	inserted, err := connection.InsertOperation(context.Background(), newInsertRequest("users", map[string]any{"name": "Ada"}, "_id", true))
	if err != nil || inserted.ID != id || inserted.Affected != 1 { t.Fatalf("Mongo insert: %#v %v", inserted, err) }
	updated, err := connection.UpdateOperation(context.Background(), updateRequestForID(id))
	if err != nil || updated.Matched != 3 || updated.Modified != 2 || updated.Count() != 2 { t.Fatalf("Mongo update: %#v %v", updated, err) }
}

func TestNeoInsertGetIdRequiresApplicationPrimaryKey(t *testing.T) {
	_, err := connection.InsertOperation(context.Background(), newInsertRequest("User", map[string]any{"name": "Ada"}, "id", true))
	if !errors.Is(err, ErrInsertIDUnavailable) { t.Fatalf("Neo 无应用主键不能伪造 ID: %v", err) }
}
```

- [ ] **Step 2: 运行并确认旧常量/计数行为失败**

Run: `go test ./framework/db -run 'TestMongoOperationResults|TestNeoInsertGetIdRequires' -count=1`

Expected: FAIL，Mongo 返回常量 1 或 Neo 返回 count 作为 ID。

- [ ] **Step 3: 实现 NoSQL operation adapters**

```go
func (c *MongoConnection) InsertOperation(ctx context.Context, request InsertRequest) (InsertResult, error) {
	result, err := collection.InsertOne(ctx, request.Data())
	if err != nil { return InsertResult{}, err }
	return InsertResult{Affected: 1, ID: result.InsertedID, IDKnown: true, Data: request.Data()}, nil
}

func (c *MongoConnection) UpdateOperation(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	result, err := collection.UpdateMany(ctx, filter, bson.M{"$set": request.Data()})
	if err != nil { return UpdateResult{}, err }
	return UpdateResult{Affected: result.ModifiedCount, Matched: result.MatchedCount, Modified: result.ModifiedCount, MatchedKnown: true, ModifiedKnown: true, Data: request.Data()}, nil
}

func (c *Neo4jConnection) InsertOperation(ctx context.Context, request InsertRequest) (InsertResult, error) {
	data := request.Data()
	if request.WantsID() {
		id, ok := data[request.PrimaryKey()]
		if !ok || isZeroDBValue(id) { return InsertResult{}, ErrInsertIDUnavailable }
	}
	// 参数化 CREATE 后返回应用主键；普通 Insert 只返回 Affected=1。
	return c.createNode(ctx, request)
}
```

Predicate 的 raw clause 在 Mongo/Neo operation 中返回 `ErrUnsafeExpression`；validated clause 暂复用现有 parser，输入只能来自私有 Predicate 构造器。Neo Update 填 `Affected=matched, MatchedKnown=true, ModifiedKnown=false`，不伪造 modified。

- [ ] **Step 4: 运行 NoSQL 测试与 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestMongo|TestNeo' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/mongo_connection.go framework/db/neo4j_connection.go framework/db/mongo_connection_io_test.go framework/db/neo4j_connection_test.go
git commit -m "fix(db): preserve NoSQL operation results"
```

### Task 6: 发布 ThinkPHP API 并删除 legacy Connection

**Files:**
- Modify: `framework/db/connection.go`
- Modify: `framework/db/query_execute.go`
- Modify: `framework/db/query_batch.go`
- Modify: `framework/db/sql_connection.go`
- Modify: `framework/db/mongo_connection.go`
- Modify: `framework/db/neo4j_connection.go`
- Modify: all `framework/db/**/*_test.go` connection doubles and call sites
- Test: `framework/db/query_thinkphp_test.go`

**Interfaces:**
- Produces final: `Connection.Select(ctx, SelectRequest)`、`Insert`、`Update`、`Delete`、`Count`；Query `Save/Insert/InsertGetId/InsertAll/Update/UpdateResult/Delete/DeleteResult`。

- [ ] **Step 1: 写最终公共 API 失败测试**

```go
func TestThinkPHPWriteAPIContract(t *testing.T) {
	q := NewDB(&operationRecorder{insertID: "01JABC"}).Table("users")
	if n, err := q.Insert(map[string]any{"name": "Ada"}); err != nil || n != 1 { t.Fatalf("Insert=%d,%v", n, err) }
	if id, err := q.InsertGetId(map[string]any{"name": "Lin"}); err != nil || id != "01JABC" { t.Fatalf("InsertGetId=%#v,%v", id, err) }
	if n, err := q.WhereField("id", "=", 7).Save(map[string]any{"name": "Grace"}); err != nil || n != 1 { t.Fatalf("Save update=%d,%v", n, err) }
	if n, err := q.Save(map[string]any{"name": "New"}, true); err != nil || n != 1 { t.Fatalf("Save insert=%d,%v", n, err) }
}
```

- [ ] **Step 2: 运行并确认旧 Insert 语义失败**

Run: `go test ./framework/db -run 'TestThinkPHPWriteAPIContract' -count=1`

Expected: FAIL，`InsertGetId`/`Save` 不存在或 Insert 返回 ID。

- [ ] **Step 3: 实现最终 API 并移除 legacy**

```go
func (q *Query) Insert(data map[string]any) (int64, error) {
	result, err := q.insertResult(data, false)
	if err != nil { return 0, err }
	return result.Affected, nil
}

func (q *Query) InsertGetId(data map[string]any) (any, error) {
	result, err := q.insertResult(data, true)
	if err != nil { return nil, err }
	return result.InsertedID()
}

func (q *Query) Save(data map[string]any, forceInsert ...bool) (int64, error) {
	if len(forceInsert) > 1 { return 0, q.reportError("save", ErrInvalidQuery, nil) }
	if len(forceInsert) == 1 && forceInsert[0] || q.predicate.Empty() { return q.Insert(data) }
	return q.Update(data)
}

func (q *Query) Update(data map[string]any) (int64, error) {
	result, err := q.UpdateResult(data)
	if err != nil { return 0, err }
	return result.Count(), nil
}

func (q *Query) Delete() (int64, error) {
	result, err := q.DeleteResult()
	if err != nil { return 0, err }
	return result.Deleted, nil
}
```

把 `OperationConnection` 方法改名为最终 Connection 方法并删除旧接口、`ContextualConnection` 和低层 `where []string` 方法。`operationRecorder` 统一声明 `rows []map[string]any`、`insertID any`、`capabilities DriverCapabilities` 和 `insertCalls atomic.Int64`；Insert 中先 `insertCalls.Add(1)`，并用 `insertID` 返回 ID。所有其他测试桩按以下方法集迁移：

```go
func (c *recordingConnection) Select(ctx context.Context, r SelectRequest) ([]map[string]any, error) { return c.rows, nil }
func (c *recordingConnection) Insert(ctx context.Context, r InsertRequest) (InsertResult, error) { return InsertResult{Affected: 1, ID: c.insertID, IDKnown: r.WantsID()}, nil }
func (c *recordingConnection) Update(ctx context.Context, r UpdateRequest) (UpdateResult, error) { return UpdateResult{Affected: 1}, nil }
func (c *recordingConnection) Delete(ctx context.Context, r DeleteRequest) (DeleteResult, error) { return DeleteResult{Deleted: 1}, nil }
func (c *recordingConnection) Count(ctx context.Context, r CountRequest) (int64, error) { return int64(len(c.rows)), nil }
func (c *recordingConnection) Close() error { return nil }
```

全仓迁移需要主键的调用到 `InsertGetId`；只检查成功/行数的调用保留 `Insert`。

- [ ] **Step 4: 运行全 ORM 编译、API 和 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db/... -count=1`

Expected: PASS，且 `rg -n 'ContextualConnection|where \[\]string|InsertContextWithPrimaryKey' framework/db -g '*.go'` 无生产代码命中。

- [ ] **Step 5: 提交**

```powershell
git add framework/db
git commit -m "feat(db): align write APIs with ThinkPHP"
```

### Task 7: 阶段验证与审计状态更新

**Files:**
- Modify: `docs/数据库/ORM源码审计-2026-07-13.md`

**Interfaces:**
- Consumes: Tasks 1-6。
- Produces: T3-01、T3-02、T3-07、NQ-01、NQ-08 的“源码已修复”证据，真实服务边界仍明确。

- [ ] **Step 1: 运行定向测试并记录测试名**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db/... -run 'TestThinkPHPWriteAPIContract|TestQueryBranches|TestQueryConcurrent|TestSQLiteSimplePathAggregates|TestMongoOperationResults|TestNeoInsertGetIdRequires' -count=1`

Expected: PASS。

- [ ] **Step 2: 运行本阶段静态验证**

Run: `go vet ./framework/db/...`

Expected: PASS，无输出。

- [ ] **Step 3: 在五项 finding 下追加结构化状态**

先运行 `git log --format='%h %s' --grep='feat(db):' -6`，再为每项写入实际输出中的直接修复提交。使用以下固定结构，`abcdef1` 必须替换为命令实际返回的 7 位 hash，每项只列直接相关测试：

```markdown
**修复状态（2026-07-14）**：源码已修复。

- 修复提交：`abcdef1`
- 回归测试：`TestThinkPHPWriteAPIContract`
- 真实驱动边界：MongoDB/Neo4j 服务端行为待 live-server 矩阵复核；源码契约与 mock I/O 已固定。
```

- [ ] **Step 4: 检查审计编号和空白**

Run: `git diff --check; rg -n 'T3-01|T3-02|T3-07|NQ-01|NQ-08' docs/数据库/ORM源码审计-2026-07-13.md`

Expected: diff check PASS，五项均出现新增修复状态。

- [ ] **Step 5: 提交**

```powershell
git add docs/数据库/ORM源码审计-2026-07-13.md
git commit -m "docs: record ORM API contract fixes"
```
