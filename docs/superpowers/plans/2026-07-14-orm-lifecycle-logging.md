# ORM 生命周期、所有权与日志安全 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保证事务、连接 lease 和 Manager 关闭 exactly-once，阻止驱动错误及 SQL 字面量进入日志，并为 ChunkById 建立可证明的游标顺序契约。

**Architecture:** Tx 使用单一 finalize 状态机协调 Commit/Rollback/context watcher；DB 只通过受控回调暴露连接租约；Manager 按稳定 ConnectionID 共用 managed handle。错误原文只返回调用方，日志记录安全分类；游标比较通过显式 codec 而不是通用字符串比较。

**Tech Stack:** Go 1.26.5、`database/sql`、`context`、`sync`/`atomic`、Go race detector。

## Global Constraints

- context 自动回滚必须设置框架终态并立即释放 lease，调用方无需补一次 Rollback。
- 原始 error 保留在返回链中，但 logger message/context 不得包含未知驱动错误文本。
- 不使用反射地址猜测连接身份；ConnectionID 必须稳定且显式。
- `GetConnection` 删除，连接借用使用 `WithConnection`。
- 文本/字节游标没有显式 codec 时必须拒绝，不能声称与数据库 collation 一致。
- 不增加配置文件，不修改主工作区 `.env.example`。
- 每项先写失败测试，再修改生产代码。

---

## File Structure

- Create `framework/db/connection_identity.go`: 稳定 ConnectionID 与 managed handle。
- Create `framework/db/cursor.go`: `CursorCodec` 和默认有序类型实现。
- Modify `framework/db/errors.go`: 新增稳定的 `ErrUnsupportedCursorKey`。
- Modify `framework/db/connection.go`: ConnectionID 契约。
- Modify `framework/db/db.go`: `WithConnection`、managed handle、安全日志上下文。
- Modify `framework/db/manager.go`: 按 ConnectionID 共用/关闭 handle。
- Modify `framework/db/transaction.go`: context watcher 与 finalize-once 状态机。
- Modify `framework/db/sql_text.go`: 方言感知、保守 SQL token 脱敏。
- Modify `framework/db/query_batch.go`: codec 驱动的 ChunkById。
- Modify `framework/db/builder.go` and five builders: `DialectName()` 供日志脱敏选择。
- Modify all Connection test doubles: 提供稳定 ConnectionID。

### Task 1: 事务 context 终态与 exactly-once finalize（T2-01）

**Files:**
- Modify: `framework/db/transaction.go`
- Test: `framework/db/transaction_hardening_test.go`
- Test: `framework/db/db_lifecycle_hardening_test.go`

**Interfaces:**
- Produces: `transactionCommitted`、`transactionRolledBack`、`transactionContextRolledBack`、`transactionFinalizationFailed` 终态；`Tx.Done()` 只读通道。

- [ ] **Step 1: 写自动回滚后 Close 不阻塞和竞争失败测试**

```go
func TestBeginTxContextCancellationReleasesLease(t *testing.T) {
	database, driver := newLifecycleDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := database.BeginTx(ctx, nil)
	if err != nil { t.Fatal(err) }
	cancel()
	select {
	case <-tx.Done():
	case <-time.After(time.Second): t.Fatal("context 回滚未终结 wrapper")
	}
	closed := make(chan error, 1)
	go func() { closed <- database.Close() }()
	select {
	case err := <-closed: if err != nil { t.Fatal(err) }
	case <-time.After(time.Second): t.Fatal("Close 仍等待泄漏 lease")
	}
	if got := driver.rollbackCalls.Load(); got != 1 { t.Fatalf("rollback=%d", got) }
}

func TestCommitAndCancellationFinalizeOnce(t *testing.T) {
	for i := 0; i < 100; i++ {
		tx, cancel, driver := newRacingTransaction(t)
		var wg sync.WaitGroup; wg.Add(2)
		go func() { defer wg.Done(); _ = tx.Commit() }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait(); <-tx.Done()
		if got := driver.commitCalls.Load() + driver.rollbackCalls.Load(); got != 1 { t.Fatalf("finalize=%d", got) }
	}
}
```

- [ ] **Step 2: 运行 race 确认 lease 泄漏/竞争失败**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestBeginTxContextCancellation|TestCommitAndCancellation' -count=1`

Expected: FAIL，Close 超时或 Tx 没有 `Done`。

- [ ] **Step 3: 实现单一 finalize 状态机和 watcher**

```go
type transactionState uint8
const (
	transactionActive transactionState = iota
	transactionCompleting
	transactionCommitted
	transactionRolledBack
	transactionContextRolledBack
	transactionFinalizationFailed
)

type transactionAction uint8
const (actionCommit transactionAction = iota; actionRollback; actionContextRollback)

func (t *Tx) Done() <-chan struct{} { return t.done }

func (t *Tx) watchContext() {
	select {
	case <-t.ctx.Done(): _ = t.finalize(actionContextRollback)
	case <-t.done:
	}
}

func (t *Tx) finalize(action transactionAction) error {
	if t == nil { return fmt.Errorf("%w: 事务不能为空", ErrInvalidTransaction) }
	t.mu.Lock()
	if t.state != transactionActive || t.tx == nil { t.mu.Unlock(); return ErrTransactionDone }
	t.state = transactionCompleting
	handle := t.tx
	t.mu.Unlock()

	var err error
	switch action {
	case actionCommit: err = handle.Commit()
	default: err = handle.Rollback()
	}

	t.mu.Lock()
	switch {
	case action == actionContextRollback:
		t.state = transactionContextRolledBack
	case action == actionCommit && err == nil:
		t.state = transactionCommitted
	case action == actionCommit && t.ctx.Err() != nil:
		t.state = transactionContextRolledBack
	case action == actionRollback && err == nil:
		t.state = transactionRolledBack
	default:
		t.state = transactionFinalizationFailed
	}
	close(t.done)
	t.mu.Unlock()
	t.releaseOnce.Do(func() { if t.release != nil { t.release() } })
	if action == actionContextRollback && errors.Is(err, sql.ErrTxDone) { return t.ctx.Err() }
	return err
}
```

`BeginTx` 初始化 `done: make(chan struct{})` 后启动 `go tx.watchContext()`；`Commit/Rollback` 只调用 `finalize`。Commit 成功后即使 context 随后取消也保持 committed；Commit 返回错误且 context 已取消时才标记 context rollback。其他 driver finalize error 标记 failed，但同样关闭 done、释放 lease；二次调用返回 `ErrTransactionDone`。

- [ ] **Step 4: 运行事务全套 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'Test.*Transaction|TestBeginTxContext|TestCommitAndCancellation' -count=1`

Expected: PASS，无 goroutine/lease 泄漏。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/transaction.go framework/db/transaction_hardening_test.go framework/db/db_lifecycle_hardening_test.go
git commit -m "fix(db): finalize canceled transactions"
```

### Task 2: 受控连接 lease，删除裸 GetConnection（T2-03）

**Files:**
- Modify: `framework/db/db.go`
- Modify: `framework/db/manager.go`
- Modify: callers/tests using `GetConnection`
- Test: `framework/db/db_lifecycle_hardening_test.go`

**Interfaces:**
- Produces: `func (db *DB) WithConnection(func(Connection) error) error`。
- Removes: `GetConnection()`。

- [ ] **Step 1: 写 callback 持有 lease 和 panic 释放测试**

```go
func TestWithConnectionHoldsLeaseUntilCallbackReturns(t *testing.T) {
	database := NewDB(newIdentityConnection("lease"))
	entered, release := make(chan struct{}), make(chan struct{})
	go func() { _ = database.WithConnection(func(Connection) error { close(entered); <-release; return nil }) }()
	<-entered
	closed := make(chan struct{}); go func() { _ = database.Close(); close(closed) }()
	select { case <-closed: t.Fatal("callback 未结束时 Close 不应完成"); case <-time.After(50 * time.Millisecond): }
	close(release)
	select { case <-closed: case <-time.After(time.Second): t.Fatal("lease 未释放") }
}

func TestWithConnectionReleasesLeaseAfterPanic(t *testing.T) {
	database := NewDB(newIdentityConnection("panic"))
	func() { defer func() { _ = recover() }(); _ = database.WithConnection(func(Connection) error { panic("boom") }) }()
	if err := database.Close(); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: 运行测试确认 API 缺失**

Run: `go test ./framework/db -run 'TestWithConnection' -count=1`

Expected: FAIL，`database.WithConnection undefined`。

- [ ] **Step 3: 实现回调 lease 并删除 GetConnection**

```go
func (db *DB) WithConnection(callback func(Connection) error) error {
	if callback == nil { return fmt.Errorf("%w: 连接回调不能为空", ErrInvalidQuery) }
	connection, release, err := db.acquireConnection()
	if err != nil { return err }
	defer release()
	return callback(connection)
}
```

删除 `GetConnection` 和 `connectionSnapshot` 的“取出后立即 release”用途；内部只读检查改为 `withConnectionSnapshot` 或在 `db.mu` 下读取 managed handle。更新 Manager.Add 和测试，不允许任何返回裸 Connection 且 lease 已结束的 API。

- [ ] **Step 4: 运行生命周期与全仓调用搜索**

Run: `go test ./framework/db -run 'TestWithConnection|TestDBClose|TestManager' -count=1; rg -n 'GetConnection\(' -g '*.go'`

Expected: 测试 PASS，`rg` 无命中。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/db.go framework/db/manager.go framework/db/*_test.go
git commit -m "fix(db): scope borrowed connection leases"
```

### Task 3: 稳定 ConnectionID 与 Manager close-once（T2-04）

**Files:**
- Create: `framework/db/connection_identity.go`
- Modify: `framework/db/connection.go`
- Modify: `framework/db/db.go`
- Modify: `framework/db/manager.go`
- Modify: `framework/db/sql_connection.go`
- Modify: `framework/db/mongo_connection.go`
- Modify: `framework/db/neo4j_connection.go`
- Modify: all Connection test doubles
- Test: `framework/db/manager_test.go`

**Interfaces:**
- Produces: `ConnectionID`, `NewConnectionID`, `Connection.ConnectionID()`；Manager 内唯一 `managedConnection`。

- [ ] **Step 1: 写两个 DB wrapper 共享连接只关闭一次的失败测试**

```go
func TestManagerClosesSharedConnectionIdentityOnce(t *testing.T) {
	connection := &identityCountingConnection{id: NewConnectionID("shared")}
	manager := NewManager("first")
	if err := manager.Add("first", NewDB(connection)); err != nil { t.Fatal(err) }
	if err := manager.Add("second", NewDB(connection)); err != nil { t.Fatal(err) }
	if err := manager.Close(); err != nil { t.Fatal(err) }
	if got := connection.closeCalls.Load(); got != 1 { t.Fatalf("Close 调用 %d 次", got) }
}
```

- [ ] **Step 2: 运行确认当前按 DB 指针去重失败**

Run: `go test ./framework/db -run 'TestManagerClosesSharedConnectionIdentityOnce' -count=1`

Expected: FAIL，`Close 调用 2 次`。

- [ ] **Step 3: 实现稳定 ID 和 managed handle**

```go
type ConnectionID string
var connectionSequence atomic.Uint64
func NewConnectionID(prefix string) ConnectionID {
	return ConnectionID(fmt.Sprintf("%s-%d", prefix, connectionSequence.Add(1)))
}

type managedConnection struct {
	id ConnectionID
	connection Connection
	closeOnce sync.Once
	closeErr error
}
func (h *managedConnection) Close() error {
	h.closeOnce.Do(func() { h.closeErr = h.connection.Close() })
	return h.closeErr
}
```

Connection 最终接口增加 `ConnectionID() ConnectionID`。SQL/Mongo/Neo 结构体持有 lazy `identity` 和 `identityOnce`；同一实例始终返回同一 ID。Manager.Add 维护 `handles map[ConnectionID]*managedConnection`，遇到相同 ID 复用 handle；Manager.Close 按名称排序后关闭唯一 handle 并 `errors.Join`。

- [ ] **Step 4: 运行 Manager、DB close 和 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestManager|TestDBClose|TestWithConnection' -count=1`

Expected: PASS，共享连接 Close 恰好一次。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/connection_identity.go framework/db/connection.go framework/db/db.go framework/db/manager.go framework/db/sql_connection.go framework/db/mongo_connection.go framework/db/neo4j_connection.go framework/db/*_test.go
git commit -m "fix(db): deduplicate managed connection ownership"
```

### Task 4: 驱动错误只返回，不进入日志（T2-02）

**Files:**
- Modify: `framework/db/db.go`
- Modify: `framework/db/transaction.go`
- Test: `framework/db/db_logging_test.go`
- Test: `framework/db/log_redaction_test.go`

**Interfaces:**
- Produces: `safeDatabaseErrorContext(error) map[string]any`；logger message 无 error 文本。

- [ ] **Step 1: 写敏感 error sentinel 不进日志测试**

```go
func TestDriverErrorTextIsReturnedButNotLogged(t *testing.T) {
	secret := "customer-token-7f3a"
	driverErr := fmt.Errorf("duplicate value %s", secret)
	logger := &captureLogger{}
	database := NewDB(&failingOperationConnection{err: driverErr}); database.SetLogger(logger)
	_, err := database.Table("users").Select()
	if !errors.Is(err, driverErr) { t.Fatalf("返回链丢失原始错误: %v", err) }
	if strings.Contains(logger.String(), secret) { t.Fatalf("日志泄露驱动文本: %s", logger.String()) }
	if !strings.Contains(logger.String(), "error_type") { t.Fatalf("日志缺少安全分类: %s", logger.String()) }
}
```

- [ ] **Step 2: 运行确认 `%v` 泄露**

Run: `go test ./framework/db -run 'TestDriverErrorTextIsReturnedButNotLogged' -count=1`

Expected: FAIL，捕获日志包含 secret。

- [ ] **Step 3: 改为安全 error 分类**

```go
type sqlStateCarrier interface { SQLState() string }

func safeDatabaseErrorContext(err error) map[string]any {
	ctx := map[string]any{"error_type": fmt.Sprintf("%T", err)}
	var state sqlStateCarrier
	if errors.As(err, &state) && regexp.MustCompile(`^[0-9A-Z]{5}$`).MatchString(state.SQLState()) {
		ctx["sql_state"] = state.SQLState()
	}
	return ctx
}

func (db *DB) reportError(operation string, err error, ctx map[string]any) error {
	if err == nil { return nil }
	logger := db.loggerSnapshot()
	if logger != nil {
		logCtx := map[string]any{"component": "db", "operation": operation}
		for k, v := range safeDatabaseErrorContext(err) { logCtx[k] = v }
		for k, v := range ctx { logCtx[k] = v }
		logger.ErrorCtx("database operation failed", logCtx)
	}
	return err
}
```

panic rollback 日志同样只调用 `reportError`；禁止任何 `fmt.Sprintf(...%v, err)` 进入 logger。

- [ ] **Step 4: 运行日志与事务 panic 回归**

Run: `go test ./framework/db -run 'Test.*Log|TestDriverErrorText|TestTransaction.*Panic' -count=1`

Expected: PASS，返回 error 保真、日志无敏感文本。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/db.go framework/db/transaction.go framework/db/db_logging_test.go framework/db/log_redaction_test.go
git commit -m "fix(db): keep driver error text out of logs"
```

### Task 5: 方言感知 SQL 脱敏（T2-05）

**Files:**
- Modify: `framework/db/builder.go`
- Modify: `framework/db/builder/mysql.go`
- Modify: `framework/db/builder/pgsql.go`
- Modify: `framework/db/builder/sqlite.go`
- Modify: `framework/db/builder/sqlsrv.go`
- Modify: `framework/db/builder/oracle.go`
- Modify: `framework/db/sql_text.go`
- Modify: `framework/db/db.go`
- Test: `framework/db/log_redaction_test.go`

**Interfaces:**
- Produces: `Builder.DialectName() string`；`redactSQLText(sql, dialect string)`。

- [ ] **Step 1: 写双引号在 MySQL/未知模式必须脱敏的测试**

```go
func TestSQLRedactionTreatsDoubleQuotesByDialect(t *testing.T) {
	const secret = "double-quoted-secret"
	if got := redactSQLText(`SELECT "double-quoted-secret"`, "mysql"); strings.Contains(got, secret) { t.Fatalf("MySQL 双引号泄露: %s", got) }
	if got := redactSQLText(`SELECT "double-quoted-secret"`, ""); strings.Contains(got, secret) { t.Fatalf("未知方言必须保守脱敏: %s", got) }
	if got := redactSQLText(`SELECT "account_id" FROM "users"`, "postgres"); !strings.Contains(got, `"account_id"`) { t.Fatalf("PostgreSQL 标识符不应破坏: %s", got) }
}
```

- [ ] **Step 2: 运行确认当前无方言参数且泄露**

Run: `go test ./framework/db -run 'TestSQLRedactionTreatsDoubleQuotesByDialect' -count=1`

Expected: FAIL，函数签名不匹配或 secret 保留。

- [ ] **Step 3: 给扫描器增加 dialect mode**

```go
func doubleQuoteIsIdentifier(dialect string) bool {
	switch strings.ToLower(dialect) {
	case "postgres", "sqlserver", "oracle": return true
	default: return false
	}
}
```

扫描到 `"` 时：`doubleQuoteIsIdentifier` 为 true 才按转义规则保留；否则像单引号一样只输出 `"[REDACTED]"`。Builder 分别返回 `mysql/postgres/sqlite/sqlserver/oracle`；DB/Query 从当前 SQLConnection.Builder 获取方言，无法识别时传空串采用保守策略。反引号和方括号只在对应方言保留，否则也脱敏。

- [ ] **Step 4: 运行全部 SQL token/redaction 测试**

Run: `go test ./framework/db -run 'TestSQLRedaction|TestCountSQLPlaceholders|TestLog' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/builder.go framework/db/builder framework/db/sql_text.go framework/db/db.go framework/db/log_redaction_test.go
git commit -m "fix(db): redact SQL with dialect aware tokens"
```

### Task 6: ChunkById 使用显式 CursorCodec（T2-06）

**Files:**
- Create: `framework/db/cursor.go`
- Modify: `framework/db/errors.go`
- Modify: `framework/db/data.go`
- Modify: `framework/db/query_batch.go`
- Modify: `framework/db/model_query.go`
- Test: `framework/db/batch_chunk_test.go`
- Test: `framework/db/data_value_hardening_test.go`

**Interfaces:**
- Produces: `CursorCodec`、`OrderedCursorCodec`、`ChunkByIdWithCodec`；默认只支持整数和 `time.Time`。

- [ ] **Step 1: 写文本游标拒绝与自定义 codec 成功测试**

```go
func TestChunkByIdRejectsTextWithoutCodec(t *testing.T) {
	database := NewDB(&chunkRecorderConn{rows: [][]map[string]any{{{"id": "a"}, {"id": "b"}}}})
	err := database.Table("users").ChunkById(2, "id", func([]map[string]any) bool { return true })
	if !errors.Is(err, ErrUnsupportedCursorKey) { t.Fatalf("文本游标应拒绝: %v", err) }
}

type lexicalCursorCodec struct{}
func (lexicalCursorCodec) Compare(a, b any) (int, error) { return strings.Compare(a.(string), b.(string)), nil }

func TestChunkByIdAllowsExplicitTextCodec(t *testing.T) {
	err := database.Table("users").ChunkByIdWithCodec(2, "id", lexicalCursorCodec{}, func([]map[string]any) bool { return true })
	if err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: 运行确认通用字符串比较未拒绝**

Run: `go test ./framework/db -run 'TestChunkByIdRejectsTextWithoutCodec|TestChunkByIdAllowsExplicitTextCodec' -count=1`

Expected: FAIL，默认文本游标被接受或新 API 不存在。

- [ ] **Step 3: 实现 codec 驱动游标推进**

```go
// errors.go
var ErrUnsupportedCursorKey = errors.New("database cursor key requires an explicit ordering codec")

type CursorCodec interface { Compare(previous, next any) (int, error) }
type OrderedCursorCodec struct{}
func (OrderedCursorCodec) Compare(previous, next any) (int, error) {
	if _, ok := previous.(string); ok { return 0, ErrUnsupportedCursorKey }
	if _, ok := previous.([]byte); ok { return 0, ErrUnsupportedCursorKey }
	return compareOrderedDatabaseCursor(previous, next)
}

func (q *Query) ChunkById(count int, pk string, callback func([]map[string]any) bool) error {
	return q.ChunkByIdWithCodec(count, pk, OrderedCursorCodec{}, callback)
}
func (q *Query) ChunkByIdWithCodec(count int, pk string, codec CursorCodec, callback func([]map[string]any) bool) error {
	if codec == nil { return q.reportError("chunk_by_id", ErrUnsupportedCursorKey, nil) }
	return q.chunkByID(count, pk, codec, callback)
}
```

内部进度判断只调用 codec；数据库仍通过 `WHERE pk > ? ORDER BY pk` 决定排序。ModelQuery 增加同名转发 API。

- [ ] **Step 4: 运行 chunk、SQLite 与 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db/... -run 'TestChunkById|TestDatabaseCursor' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/errors.go framework/db/cursor.go framework/db/data.go framework/db/query_batch.go framework/db/model_query.go framework/db/batch_chunk_test.go framework/db/data_value_hardening_test.go
git commit -m "fix(db): require explicit cursor ordering"
```

### Task 7: 阶段验证与审计状态

**Files:**
- Modify: `docs/数据库/ORM源码审计-2026-07-13.md`

**Interfaces:**
- Produces: T2-01 至 T2-06 的源码修复证据。

- [ ] **Step 1: 运行生命周期/安全定向 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestBeginTxContext|TestCommitAndCancellation|TestWithConnection|TestManagerClosesShared|TestDriverErrorText|TestSQLRedaction|TestChunkById' -count=1`

Expected: PASS。

- [ ] **Step 2: 运行 ORM vet**

Run: `go vet ./framework/db/...`

Expected: PASS，无输出。

- [ ] **Step 3: 更新六项审计状态**

Run: `git log --oneline -7`

Expected: 输出本计划六个修复提交；把每项直接对应的 hash、测试名和“源码已修复”写入 T2-01 至 T2-06，T2-06 说明文本游标需要显式 codec。

- [ ] **Step 4: 校验文档与空白**

Run: `git diff --check; rg -n '### T2-0[1-6]|修复状态' docs/数据库/ORM源码审计-2026-07-13.md`

Expected: PASS，六项均有修复状态。

- [ ] **Step 5: 提交**

```powershell
git add docs/数据库/ORM源码审计-2026-07-13.md
git commit -m "docs: record ORM lifecycle fixes"
```
