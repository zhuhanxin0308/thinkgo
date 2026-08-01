# ORM SQL 方言与连接器一致性 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让五种 SQL 方言对锁、分页、命名和连接参数给出正确 SQL 或明确错误，并修复 MySQL 配置绕过及 SQLite 内存库过期丢失。

**Architecture:** 锁从字符串改为 `LockMode + LockSpec`，由 builder 分别生成 table hint 或尾子句；Builder 方法允许返回 capability error。MySQL 使用官方 typed Config 构造 DSN；SQLite `:memory:` 绕过通用池过期设置。

**Tech Stack:** Go 1.26.5、`database/sql`、go-sql-driver/mysql v1.10.0、go-sqlite3 v1.14.47、五方言 builders、Oracle build tag。

## Global Constraints

- 不支持的锁/分页组合必须返回 `ErrUnsupportedFeature`，不能返回空字符串假装成功。
- MySQL lock 只接受 typed enum；不存在任意字符串下沉路径。
- SQL Server 锁必须出现在 table reference 的 `WITH (...)`，不能放到 SQL 尾部。
- Oracle 普通逻辑名遵循默认大写对象规则；显式原生 SQL 是保留精确大小写的边界。
- MySQL 高风险布尔参数所有合法真值都拒绝；冒号用户名在连接前拒绝。
- SQLite `:memory:` 固定 1/1 连接且 lifetime/idle time 均为 0。
- 不新增配置文件；真实服务不可用时明确记录验证边界。

---

## File Structure

- Modify `framework/db/builder.go`: `LockMode`, `LockSpec`, error-returning lock contract。
- Modify `framework/db/errors.go`: 新增 `ErrUnsupportedFeature` 与 `ErrUnsupportedLockMode`。
- Modify `framework/db/query.go`, `framework/db/query_sql.go`: typed lock 和 capability propagation。
- Modify `framework/db/builder/{mysql,pgsql,sqlite,sqlsrv,oracle}.go`: 方言锁实现。
- Modify `framework/db/builder/common.go`: Oracle logical identifier helpers if shared code is needed。
- Modify `framework/db/connector/mysql.go`: typed MySQL Config、规范化参数、用户名验证。
- Modify `framework/db/connector/sqlite.go`, `sql_common.go`: 内存库专用 pool settings。
- Modify builder/connector tests and SQLite integration tests。
- Modify `docs/数据库/查询构造器.md`, `docs/数据库/连接数据库.md`, audit report capability matrix。

### Task 1: typed LockMode 与 MySQL/PostgreSQL/SQLite/Oracle 能力

**Files:**
- Modify: `framework/db/errors.go`
- Modify: `framework/db/builder.go`
- Modify: `framework/db/query.go`
- Modify: `framework/db/query_sql.go`
- Modify: `framework/db/builder/mysql.go`
- Modify: `framework/db/builder/pgsql.go`
- Modify: `framework/db/builder/sqlite.go`
- Modify: `framework/db/builder/oracle.go`
- Test: `framework/db/builder/dialect_test.go`
- Test: `framework/db/builder/hardening_test.go`

**Interfaces:**
- Produces: `LockMode`, `LockSpec`, `Builder.Lock(LockMode) (LockSpec, error)`。
- Removes: `LockClause(string) string`。

- [ ] **Step 1: 写锁白名单/unsupported 失败测试**

```go
func TestTypedLockCapabilities(t *testing.T) {
	tests := []struct { name string; b db.Builder; mode db.LockMode; tail string; wantErr error }{
		{"mysql update", &Mysql{}, db.LockForUpdate, " FOR UPDATE", nil},
		{"mysql share", &Mysql{}, db.LockForShare, " LOCK IN SHARE MODE", nil},
		{"pgsql share", &Pgsql{}, db.LockForShare, " FOR SHARE", nil},
		{"sqlite", &Sqlite{}, db.LockForUpdate, "", db.ErrUnsupportedFeature},
	}
	for _, tt := range tests {
		spec, err := tt.b.Lock(tt.mode)
		if !errors.Is(err, tt.wantErr) || spec.Tail != tt.tail { t.Fatalf("%s: %#v %v", tt.name, spec, err) }
	}
}
```

Oracle tagged test额外断言 `LockForShare` 返回 unsupported，不再静默提升为排他锁。

- [ ] **Step 2: 运行确认旧字符串接口失败**

Run: `go test ./framework/db/builder -run 'TestTypedLockCapabilities' -count=1`

Expected: FAIL，缺少 `LockMode`/`Builder.Lock`。

- [ ] **Step 3: 实现 typed lock contract**

```go
// errors.go
var (
	ErrUnsupportedFeature  = errors.New("database feature is unsupported by the selected driver")
	ErrUnsupportedLockMode = errors.New("database lock mode is unsupported")
)

type LockMode uint8
const (
	LockNone LockMode = iota
	LockForUpdate
	LockForShare
)
type LockSpec struct { TableHint string; Tail string }

// Builder
Lock(mode LockMode) (LockSpec, error)
```

MySQL/PostgreSQL 只对三种 enum 返回固定尾子句；SQLite 对非 None 返回 `ErrUnsupportedFeature`；Oracle 仅支持 None/Update，共享锁返回 `ErrUnsupportedFeature`。`Query.Lock(exclusive ...bool)` 映射到 enum；BuildSelectSQL 在接触驱动前传播 error。

- [ ] **Step 4: 运行 builder/query 锁回归**

Run: `go test ./framework/db ./framework/db/builder -run 'Test.*Lock' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/errors.go framework/db/builder.go framework/db/query.go framework/db/query_sql.go framework/db/builder framework/db/builder/*_test.go framework/db/*_test.go
git commit -m "fix(db): make lock capabilities explicit"
```

### Task 2: SQL Server table hints

**Files:**
- Modify: `framework/db/builder/sqlsrv.go`
- Modify: `framework/db/query_sql.go`
- Test: `framework/db/builder/dialect_test.go`
- Test: `framework/db/query_hardening_test.go`

**Interfaces:**
- Consumes: `LockSpec.TableHint`。
- Produces: update lock `WITH (UPDLOCK, ROWLOCK)`；share lock `WITH (HOLDLOCK, ROWLOCK)`。

- [ ] **Step 1: 写 hint 位置失败测试**

```go
func TestSQLServerLockHintIsAttachedToTable(t *testing.T) {
	query, _, err := databaseWithBuilder(&Sqlsrv{}).Table("users").WhereField("id", "=", 7).Lock().BuildSelectSQL()
	if err != nil { t.Fatal(err) }
	want := "FROM [users] WITH (UPDLOCK, ROWLOCK) WHERE [id] = @p1"
	if !strings.Contains((&Sqlsrv{}).Rebind(query), want) { t.Fatalf("hint 位置错误: %s", query) }
	if strings.HasSuffix(query, "WITH (UPDLOCK, ROWLOCK)") { t.Fatalf("hint 不能在尾部: %s", query) }
}
```

- [ ] **Step 2: 运行确认 SQL Server 静默无锁**

Run: `go test ./framework/db ./framework/db/builder -run 'TestSQLServerLockHintIsAttachedToTable' -count=1`

Expected: FAIL，SQL 不含 `WITH (UPDLOCK, ROWLOCK)`。

- [ ] **Step 3: 实现 SQL Server LockSpec 并在 FROM 后插入**

```go
func (s *Sqlsrv) Lock(mode db.LockMode) (db.LockSpec, error) {
	switch mode {
	case db.LockNone: return db.LockSpec{}, nil
	case db.LockForUpdate: return db.LockSpec{TableHint: " WITH (UPDLOCK, ROWLOCK)"}, nil
	case db.LockForShare: return db.LockSpec{TableHint: " WITH (HOLDLOCK, ROWLOCK)"}, nil
	default: return db.LockSpec{}, db.ErrUnsupportedLockMode
	}
}
```

`BuildSelectSQL` 在写入 quoted main table 后立刻追加 `TableHint`，join table 不自动继承；尾部仅追加 `Tail`。

- [ ] **Step 4: 运行 SQL Server pagination/lock 组合测试**

Run: `go test ./framework/db ./framework/db/builder -run 'TestSQLServer|TestPaginationDialects' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/builder/sqlsrv.go framework/db/query_sql.go framework/db/builder/dialect_test.go framework/db/query_hardening_test.go
git commit -m "fix(db): compile SQL Server lock hints"
```

### Task 3: Oracle 分页锁拒绝和默认命名规则

**Files:**
- Modify: `framework/db/builder/oracle.go`
- Modify: `framework/db/query_sql.go`
- Test: `framework/db/builder/oracle_batch_test.go`
- Test: `framework/db/builder/dialect_test.go` under oracle tag

**Interfaces:**
- Produces: Oracle logical identifier uppercase quoting；pagination+lock explicit error。

- [ ] **Step 1: 写 Oracle tagged 失败测试**

```go
func TestOracleLogicalIdentifiersAndLockedPagination(t *testing.T) {
	b := &Oracle{}
	if got := b.QuoteIdentifier("app.users"); got != `"APP"."USERS"` { t.Fatalf("Oracle logical name=%s", got) }
	q := databaseWithBuilder(b).Name("users").WhereField("status", "=", 1).Order("id").Limit(10).Lock()
	if _, _, err := q.BuildSelectSQL(); !errors.Is(err, db.ErrUnsupportedFeature) { t.Fatalf("分页锁应明确拒绝: %v", err) }
}
```

- [ ] **Step 2: 运行 tagged test 确认小写 quoted/非法 SQL**

Run: `$env:CGO_ENABLED='1'; go test -tags oracle ./framework/db ./framework/db/builder -run 'TestOracleLogicalIdentifiersAndLockedPagination' -count=1`

Expected: FAIL，得到 `"app"."users"` 或无错误。

- [ ] **Step 3: 实现 Oracle logical normalization 和组合校验**

```go
func oracleLogicalIdentifier(name string) string {
	parts := strings.Split(name, ".")
	for i := range parts { parts[i] = strings.ToUpper(parts[i]) }
	return quoteWith(strings.Join(parts, "."), `"`, `"`)
}
```

Oracle 的 `QuoteIdentifier`、fields、order、group、RETURNING 和 batch 全部复用逐段大写引用。`BuildSelectSQL` 在 `DialectName()=="oracle" && lockMode!=LockNone && (limit>0 || offset>0)` 时返回 `ErrUnsupportedFeature`，不生成 `OFFSET/FETCH ... FOR UPDATE`。精确大小写对象只允许调用方走显式 raw SQL。

- [ ] **Step 4: 运行 Oracle tagged builder/connector compile**

Run: `$env:CGO_ENABLED='1'; go test -tags oracle ./framework/db/... -run 'TestOracle|TestQuoteMultipartIdentifier' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/builder/oracle.go framework/db/query_sql.go framework/db/builder/*_test.go framework/db/*_test.go
git commit -m "fix(db): enforce safe Oracle query combinations"
```

### Task 4: MySQL typed Config 与危险参数规范化

**Files:**
- Modify: `framework/db/connector/mysql.go`
- Test: `framework/db/connector/mysql_test.go`
- Test: `framework/db/connector/hardening_test.go`

**Interfaces:**
- Produces: `buildMysqlConfig(Config) (*mysql.Config, error)`；`buildMysqlDSN(Config) (string, error)`。

- [ ] **Step 1: 写所有真值、大小写冲突和冒号用户名测试**

```go
func TestMysqlRejectsEveryDangerousTrueValue(t *testing.T) {
	for _, value := range []string{"1", "t", "T", "TRUE", "True", "true"} {
		for _, key := range []string{"multiStatements", "allowAllFiles", "allowCleartextPasswords", "allowFallbackToPlaintext"} {
			if _, err := buildMysqlDSN(mysqlConfigWithParam(key, value)); !errors.Is(err, db.ErrInvalidDatabaseConfig) { t.Fatalf("%s=%s 未拒绝: %v", key, value, err) }
		}
	}
}

func TestMysqlRejectsCanonicalKeyDuplicatesAndColonUsername(t *testing.T) {
	if _, err := buildMysqlDSN(mysqlConfigWithParams(map[string]string{"multiStatements": "false", "MULTISTATEMENTS": "false"})); !errors.Is(err, db.ErrInvalidDatabaseConfig) { t.Fatal(err) }
	config := validMysqlConfig(); config.Username = "tenant:admin"
	if _, err := buildMysqlDSN(config); !errors.Is(err, db.ErrInvalidDatabaseConfig) { t.Fatalf("冒号用户名应拒绝: %v", err) }
}
```

- [ ] **Step 2: 运行确认 blocklist 绕过**

Run: `go test ./framework/db/connector -run 'TestMysqlRejectsEvery|TestMysqlRejectsCanonical' -count=1`

Expected: FAIL，至少 `1`/大小写键或冒号用户名被接受。

- [ ] **Step 3: 使用官方 typed Config 构建**

```go
func buildMysqlConfig(config db.Config) (*mysqlDriver.Config, error) {
	if strings.Contains(config.Username, ":") { return nil, fmt.Errorf("%w: MySQL 用户名不能包含冒号", db.ErrInvalidDatabaseConfig) }
	mc := mysqlDriver.NewConfig()
	mc.User, mc.Passwd, mc.Net = config.Username, config.Password, "tcp"
	mc.Addr, mc.DBName = joinHostPort(config.Hostname, config.Hostport), config.Database
	mc.CheckConnLiveness, mc.ParseTime, mc.Loc, mc.TLSConfig = true, true, time.Local, "true"
	mc.Params = map[string]string{"charset": defaultString(config.Charset, "utf8mb4")}
	seen := map[string]string{}
	for key, value := range config.Params {
		canonical := strings.ToLower(strings.TrimSpace(key))
		if previous, ok := seen[canonical]; ok { return nil, fmt.Errorf("%w: MySQL 参数 %q 与 %q 重复", db.ErrInvalidDatabaseConfig, previous, key) }
		seen[canonical] = key
		if isDangerousMysqlBoolean(canonical) {
			enabled, err := strconv.ParseBool(strings.TrimSpace(value)); if err != nil || enabled { return nil, fmt.Errorf("%w: MySQL 高风险参数 %s 非法", db.ErrInvalidDatabaseConfig, key) }
			continue
		}
		if err := applyMysqlTypedParameter(mc, canonical, value); err != nil { return nil, err }
	}
	return mc, nil
}
```

`applyMysqlTypedParameter` 明确处理 tls、parseTime、loc、checkConnLiveness、clientFoundRows；其他通过通用 Config 参数白名单后放入 Params。`Connect` 处理 `buildMysqlDSN` error 后才调用 `openSQLConnection`。

- [ ] **Step 4: 运行 MySQL connector 全套测试**

Run: `go test ./framework/db/connector -run 'TestMysql|TestBuildMysql|TestNetworkConnector' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/connector/mysql.go framework/db/connector/mysql_test.go framework/db/connector/hardening_test.go
git commit -m "fix(db): harden MySQL driver configuration"
```

### Task 5: SQLite `:memory:` 永不过期

**Files:**
- Modify: `framework/db/connector/sql_common.go`
- Modify: `framework/db/connector/sqlite.go`
- Test: `framework/db/connector/hardening_test.go`
- Test: `framework/db/connector/sqlite_orm_integration_test.go`

**Interfaces:**
- Produces: `sqlitePoolConfig(Config) (sqlConnectionPoolConfig, error)`；`verifySQLConnectionWithPool`。

- [ ] **Step 1: 写 pool 配置与真实丢库失败测试**

```go
func TestSqliteMemoryPoolDisablesExpiry(t *testing.T) {
	settings, err := sqlitePoolConfig(db.Config{Database: ":memory:", ConnMaxLifetimeSeconds: 1, ConnMaxIdleTimeSeconds: 1})
	if err != nil { t.Fatal(err) }
	if settings.MaxOpenConns != 1 || settings.MaxIdleConns != 1 || settings.ConnMaxLifetime != 0 || settings.ConnMaxIdleTime != 0 { t.Fatalf("memory pool=%#v", settings) }
}

func TestSqliteMemoryDatabaseSurvivesConfiguredExpiry(t *testing.T) {
	database := openMemorySQLite(t, 1, 1)
	defer database.Close()
	if _, err := database.Execute("CREATE TABLE keepalive (id INTEGER PRIMARY KEY)"); err != nil { t.Fatal(err) }
	time.Sleep(1100 * time.Millisecond)
	if _, err := database.Query("SELECT id FROM keepalive"); err != nil { t.Fatalf("内存库被过期连接丢失: %v", err) }
}
```

- [ ] **Step 2: 运行确认通用 5m/2m 或配置值仍生效**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db/connector -run 'TestSqliteMemoryPoolDisablesExpiry|TestSqliteMemoryDatabaseSurvivesConfiguredExpiry' -count=1`

Expected: FAIL，settings lifetime/idle 非 0 或表丢失。

- [ ] **Step 3: 为内存库传入专用 pool settings**

```go
func sqlitePoolConfig(config db.Config) (sqlConnectionPoolConfig, error) {
	if config.Database == ":memory:" {
		return sqlConnectionPoolConfig{MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: 0, ConnMaxIdleTime: 0}, nil
	}
	return resolveSQLConnectionPoolConfig(config)
}
```

把 `verifySQLConnection` 拆为“解析 settings”和“应用已解析 settings”两层；SQLite Connect 调用 `sqlitePoolConfig`，其他连接器保持通用设置。

- [ ] **Step 4: 运行 SQLite 全套与 race**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db/connector -run 'TestSqlite' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git add framework/db/connector/sql_common.go framework/db/connector/sqlite.go framework/db/connector/hardening_test.go framework/db/connector/sqlite_orm_integration_test.go
git commit -m "fix(db): keep SQLite memory connections alive"
```

### Task 6: 方言/连接器文档、审计矩阵与验证

**Files:**
- Modify: `docs/数据库/查询构造器.md`
- Modify: `docs/数据库/连接数据库.md`
- Modify: `docs/数据库/ORM源码审计-2026-07-13.md`

**Interfaces:**
- Produces: T3-03/T3-04/T3-05/T3-06/C-01/C-02/C-03 修复状态和最新能力表。

- [ ] **Step 1: 运行五方言单元和 Oracle tagged tests**

Run: `go test ./framework/db/builder ./framework/db/connector -count=1; $env:CGO_ENABLED='1'; go test -tags oracle ./framework/db/... -run 'TestOracle|TestMysql|TestSqlite|TestSQLServer' -count=1`

Expected: PASS。

- [ ] **Step 2: 运行真实 SQLite 和可用 MySQL 集成**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db/connector -run 'TestSqliteMemoryDatabaseSurvives|TestSqliteORM' -count=1`

Expected: PASS。随后使用现有本机 MySQL 凭据运行 integration tag；若凭据/服务不可用，在审计矩阵记录 `not run: service unavailable`，不能写 PASS。

- [ ] **Step 3: 更新文档准确能力表**

```markdown
| 方言 | 排他锁 | 共享锁 | 分页+锁 |
|---|---|---|---|
| MySQL | `FOR UPDATE` | `LOCK IN SHARE MODE` | 支持，需目标版本实服复核 |
| PostgreSQL | `FOR UPDATE` | `FOR SHARE` | 支持，需实服复核 |
| SQLite | 明确返回 unsupported | 明确返回 unsupported | 不支持 |
| SQL Server | `WITH (UPDLOCK, ROWLOCK)` | `WITH (HOLDLOCK, ROWLOCK)` | builder 支持，待实服复核 |
| Oracle | `FOR UPDATE` | 明确返回 unsupported | 明确返回 unsupported |
```

连接文档说明 MySQL 危险开关、冒号用户名拒绝和 SQLite 内存池 1/1/0/0；审计七项追加实际 commit/test/live-driver 状态。

- [ ] **Step 4: 校验文档、旧描述和空白**

Run: `rg -n '忽略|LockClause\(string|multiStatements=true|ConnMaxLifetime' docs/数据库 framework/db -g '*.md' -g '*.go'; git diff --check`

Expected: 无“SQLite/SQL Server 静默忽略锁”的陈旧描述，diff check PASS。

- [ ] **Step 5: 提交**

```powershell
git add docs/数据库/查询构造器.md docs/数据库/连接数据库.md docs/数据库/ORM源码审计-2026-07-13.md
git commit -m "docs: update ORM dialect capabilities"
```
