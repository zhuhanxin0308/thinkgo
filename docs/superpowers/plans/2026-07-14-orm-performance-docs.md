# ORM 性能验证、文档与最终交付 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用真实 SQLite/MySQL 与 profile 证明并合入有收益的 ORM 优化，建立 Mongo/Neo live 边界测试，并让 API、迁移指南、审计状态和最终验证证据完全同步。

**Architecture:** 基准首先作为独立测试资产提交，再按 profile 热点逐项优化；每个候选都有“满足统计门槛则合入，否则记录未合入”的确定决策。最终文档从源码和测试生成能力矩阵，审计报告保留原 finding 并追加修复证据。

**Tech Stack:** Go benchmark、benchstat、pprof、CGO SQLite、真实 MySQL、MongoDB/Neo4j integration build tag、Markdown docs。

## Global Constraints

- 性能结论至少包含真实 CGO SQLite 和本机真实 MySQL；mock/microbenchmark 只能辅助定位。
- MySQL 使用 `bench_orm_` 前缀独立表和可重复 seed，不读写业务表。
- 基准凭据只从环境变量读取，不新增/提交配置文件或凭据。
- profile 未显示热点或 benchstat 无稳定收益时，生产优化不合入，并在报告记录原因。
- 所有优化必须通过 race、正确性对照和全仓测试。
- 27 项审计 finding 都要有状态；live-driver 未运行只能标记边界，不能写成已通过。
- 最终更新查询、模型、连接、事务、NoSQL、迁移和索引文档。
- 五份计划严格按 API/Query → 生命周期/日志 → SQL 方言/连接器 → Model/NoSQL → 性能/文档顺序执行；后续计划只消费前一计划已经提交的接口，不并行修改共享 ORM 文件。

---

## File Structure

- Create `framework/db/orm_benchmark_test.go`: scan/model/getter/relation/SQLite benchmarks。
- Create `framework/db/connector/mysql_orm_benchmark_test.go`: 环境变量驱动的真实 MySQL seed/bench。
- Create `framework/db/live_nosql_integration_test.go`: `integration` tag 的 Mongo/Neo live contract。
- Create `docs/数据库/ORM性能报告-2026-07-14.md`: 基线、profile、优化对比与未合入项。
- Create `docs/数据库/ORM升级迁移指南.md`: 破坏性 API 迁移。
- Conditionally create `framework/db/model_metadata.go`: Task 2 的 profile 与 benchstat 同时达到既定门槛时创建；未达门槛时不创建，并把拒绝证据写入性能报告。
- Modify `framework/db/model.go`, `model_relation_keys.go`, `sql_connection.go`: 仅合入被证明的优化。
- Modify all database docs, audit report and `docs/README.md`；Task 5 搜索到根 `README.md` 的旧 ORM 示例时同步修改，否则在验证记录中写明“根 README 无 ORM 旧契约命中”。

### Task 1: 建立可重复 ORM 基准与真实 MySQL seed

**Files:**
- Create: `framework/db/orm_benchmark_test.go`
- Create: `framework/db/connector/mysql_orm_benchmark_test.go`
- Create: `docs/数据库/ORM性能报告-2026-07-14.md`

**Interfaces:**
- Produces: `BenchmarkScanRowsSQLite`（1k/10k/100k）、`BenchmarkStructToMap`、`BenchmarkGetterPipeline`、`BenchmarkRelationIndex`、`BenchmarkChunkPaginationSQLite`、`BenchmarkMySQLORM`（并发 1/32/256）。

- [ ] **Step 1: 写基准 harness 和数据正确性 guard**

```go
func BenchmarkScanRowsSQLite(b *testing.B) {
	for _, size := range []int{1000, 10000, 100000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			database := benchmarkSQLite(b, size)
			b.ReportAllocs(); b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := database.Query("SELECT id,name,score,created_at FROM bench_orm_users")
				if err != nil || len(rows) != size { b.Fatalf("rows=%d want=%d err=%v", len(rows), size, err) }
			}
		})
	}
}

func BenchmarkStructToMap(b *testing.B) {
	model := NewModel(benchmarkDB(), "bench_orm_users")
	value := &benchmarkUser{ID: 7, Name: "Ada", Score: 99}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ { if _, err := model.structToMap(value); err != nil { b.Fatal(err) } }
}
```

MySQL benchmark读取 `THINKGO_BENCH_MYSQL_HOST/PORT/USER/PASSWORD/DATABASE`；任一缺失时调用 `b.Skip` 并在报告记录 SKIP。seed 只执行 `CREATE TABLE IF NOT EXISTS bench_orm_users`、`TRUNCATE bench_orm_users` 和批量插入 100,000 行。`BenchmarkMySQLORM` 以 `b.Run("concurrency-1|32|256")` + `b.RunParallel` 测量 select/insert/update 组合，并记录 `DB.Stats()` 的 WaitCount/WaitDuration；只在报告比较 pool 参数，不在没有服务器连接上限与等待数据时修改通用默认值。

- [ ] **Step 2: 编译并运行 micro/SQLite 基线**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db -run '^$' -bench 'Benchmark(ScanRowsSQLite|StructToMap|GetterPipeline|RelationIndex|ChunkPaginationSQLite)$' -benchmem -count=10 | Tee-Object runtime/orm-baseline-sqlite.txt`

Expected: 所有 benchmark 完成，每项有 ns/op、B/op、allocs/op；`runtime/` 输出不提交。

- [ ] **Step 3: 运行真实 MySQL 基线**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db/connector -run '^$' -bench BenchmarkMySQLORM -benchmem -benchtime=3s -count=10 | Tee-Object runtime/orm-baseline-mysql.txt`

Expected: 本机 MySQL 环境变量已设置时完成并 seed 100,000 行；未设置时明确 SKIP，随后先配置当前 shell 环境变量再复跑，不能用 mock 替代最终数据。

- [ ] **Step 4: 写入环境、数据量和基线结果**

```markdown
## 基线环境

- Go/OS/Arch：由 `go version` 与 `go env GOOS GOARCH` 实测填写
- SQLite：CGO `github.com/mattn/go-sqlite3`
- MySQL：由 `SELECT VERSION()` 实测填写
- 数据：`bench_orm_users` 100,000 行，基准前 TRUNCATE + deterministic seed
- 原始输出：本机 `runtime/orm-baseline-*.txt`，不提交凭据或机器绝对路径
```

把实际测量表格写入报告，不使用估算值。

- [ ] **Step 5: 提交基准资产和基线报告**

```powershell
git add framework/db/orm_benchmark_test.go framework/db/connector/mysql_orm_benchmark_test.go docs/数据库/ORM性能报告-2026-07-14.md
git commit -m "test(db): add real ORM performance baselines"
```

### Task 2: profile 并按证据优化 metadata/getter

**Files:**
- Conditionally create: `framework/db/model_metadata.go`，仅当本 Task Step 1 与 Step 4 的双重门槛满足。
- Modify: `framework/db/model.go`
- Modify: `framework/db/orm_benchmark_test.go`
- Modify: `docs/数据库/ORM性能报告-2026-07-14.md`
- Test: `framework/db/model_hardening_test.go`

**Interfaces:**
- Produces when proven: immutable `modelMetadata` cache and pre-sorted getter snapshot。

- [ ] **Step 1: 采集 CPU/alloc profile**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db -run '^$' -bench 'Benchmark(StructToMap|GetterPipeline)$' -benchtime=10s -cpuprofile runtime/orm-model-cpu.pprof -memprofile runtime/orm-model-mem.pprof; go tool pprof -top runtime/orm-model-cpu.pprof; go tool pprof -top -alloc_space runtime/orm-model-mem.pprof`

Expected: 输出明确显示 `structToMap` reflection/tag 或 getter copy/sort 的占比；把 top 20 函数和占比写入报告。

- [ ] **Step 2: 写 cache 不可变性和并发失败测试**

```go
func TestModelMetadataCacheIsImmutableAndConcurrent(t *testing.T) {
	typ := reflect.TypeOf(benchmarkUser{})
	first, err := cachedModelMetadata(typ); if err != nil { t.Fatal(err) }
	first.fields[0].column = "mutated"
	second, err := cachedModelMetadata(typ); if err != nil { t.Fatal(err) }
	if second.fields[0].column == "mutated" { t.Fatal("metadata cache 暴露可变 backing data") }
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ { wg.Add(1); go func() { defer wg.Done(); _, _ = cachedModelMetadata(typ) }() }
	wg.Wait()
}
```

- [ ] **Step 3: 仅在 profile 命中时实现 immutable cache/snapshot**

```go
type modelFieldMetadata struct { index int; column string; omitEmpty bool }
type modelMetadata struct { fields []modelFieldMetadata }
var modelMetadataCache sync.Map

func cachedModelMetadata(typ reflect.Type) (modelMetadata, error) {
	if value, ok := modelMetadataCache.Load(typ); ok { return cloneModelMetadata(value.(modelMetadata)), nil }
	metadata, err := buildModelMetadata(typ); if err != nil { return modelMetadata{}, err }
	actual, _ := modelMetadataCache.LoadOrStore(typ, metadata)
	return cloneModelMetadata(actual.(modelMetadata)), nil
}
```

Model 注册 Getter 时在写锁内重建 `[]getterEntry` 的已排序 immutable snapshot；读取只复制 slice header 或原子加载 immutable snapshot，不再每行复制 map+排序。若 profile 中两条路径合计不足 5% CPU/alloc，则不增加生产 cache，只保留测试基准并在报告写“未合入：非热点”。

- [ ] **Step 4: 重跑十轮并用 benchstat 决策**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db -run '^$' -bench 'Benchmark(StructToMap|GetterPipeline)$' -benchmem -count=10 > runtime/orm-model-after.txt; benchstat runtime/orm-baseline-sqlite.txt runtime/orm-model-after.txt`

Expected: 合入条件为相关 benchmark 中至少一项 ns/op 或 allocs/op 改善 ≥5%、benchstat p<0.05，且另一项无 ≥3% 回退；否则撤销生产优化，仅提交报告结论。

- [ ] **Step 5: 运行 race 并提交实际选择**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db -run 'TestModelMetadata|TestModel.*Getter|TestModelStruct' -count=1`

Expected: PASS。随后提交：`git add framework/db docs/数据库/ORM性能报告-2026-07-14.md; git commit -m "perf(db): cache immutable model metadata"`；若门槛未满足，提交信息使用 `docs: record rejected ORM metadata optimization` 且不包含生产修改。

### Task 3: profile 并优化 scanRows 分配

**Files:**
- Modify: `framework/db/sql_connection.go`
- Modify: `framework/db/orm_benchmark_test.go`
- Modify: `docs/数据库/ORM性能报告-2026-07-14.md`
- Test: `framework/db/sql_connection_hardening_test.go`

**Interfaces:**
- Produces when proven: 每个结果集复用 scan slots，返回 map/`[]byte` 仍独立。

- [ ] **Step 1: 写行独立性回归**

```go
func TestScanRowsDoesNotShareReusedBuffers(t *testing.T) {
	rows := queryBinaryRows(t, [][]byte{[]byte("first"), []byte("second")})
	result, err := scanRows(rows); if err != nil { t.Fatal(err) }
	result[0]["payload"].([]byte)[0] = 'X'
	if string(result[1]["payload"].([]byte)) != "second" { t.Fatalf("结果行共享 buffer: %#v", result) }
}
```

- [ ] **Step 2: 采集 scan profile 和基线**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db -run '^$' -bench BenchmarkScanRowsSQLite -benchmem -benchtime=10s -memprofile runtime/orm-scan-mem.pprof; go tool pprof -top -alloc_space runtime/orm-scan-mem.pprof`

Expected: 报告 `scanRows` 中 values/scanArgs 分配占比。

- [ ] **Step 3: 仅在分配热点成立时复用 scan slots**

```go
values := make([]any, len(columns))
scanArgs := make([]any, len(columns))
for index := range values { scanArgs[index] = &values[index] }
for rows.Next() {
	for index := range values { values[index] = nil }
	if err := rows.Scan(scanArgs...); err != nil { return nil, err }
	row := make(map[string]any, len(columns))
	for index, column := range columns { row[column] = cloneScannedValue(values[index]) }
	results = append(results, row)
}
```

`cloneScannedValue` 必须复制 `[]byte`/driver binary，其他数据库标量按值保存。若 scan slices 不在 top allocation 或收益门槛不满足，撤销此生产改动。

- [ ] **Step 4: 对比真实 SQLite/MySQL 并检查门槛**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db -run '^$' -bench BenchmarkScanRowsSQLite -benchmem -count=10 > runtime/orm-scan-after.txt; benchstat runtime/orm-baseline-sqlite.txt runtime/orm-scan-after.txt; go test ./framework/db/connector -run '^$' -bench BenchmarkMySQLORM -benchmem -count=10`

Expected: allocs/op 或 bytes/op 改善 ≥5%、p<0.05，SQLite/MySQL 正确性不变；否则不合入。

- [ ] **Step 5: 运行正确性/race 并提交实际选择**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db/... -run 'TestScanRows|TestSqlite|TestMysql' -count=1`

Expected: PASS。门槛满足时提交 `perf(db): reduce row scan allocations`；不满足时只提交报告 `docs: record rejected row scan optimization`。

### Task 4: 关系/分页基准与 Mongo/Neo live contract

**Files:**
- Modify: `framework/db/orm_benchmark_test.go`
- Create: `framework/db/live_nosql_integration_test.go`
- Modify: `docs/数据库/ORM性能报告-2026-07-14.md`

**Interfaces:**
- Produces: 10^5/10^6 OFFSET vs keyset 数据；`integration` tag 的 Mongo/Neo contract tests；`BenchmarkLiveMongoMaterialization` 与 `BenchmarkLiveNeoSessionCollect`（10k/100k、并发 1/32/256）。

- [ ] **Step 1: 完成关系和分页真实基准**

```go
func BenchmarkChunkPaginationSQLite(b *testing.B) {
	for _, rows := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("offset/%d", rows), func(b *testing.B) { benchmarkOffsetChunk(b, rows) })
		b.Run(fmt.Sprintf("keyset/%d", rows), func(b *testing.B) { benchmarkKeysetChunk(b, rows) })
	}
}
```

`BenchmarkRelationIndex` 分别使用 1k/10k 父行和平均 4 个子行，验证结果数量后报告 allocs。

- [ ] **Step 2: 添加 live integration tests（无服务则 SKIP 并记录）**

```go
//go:build integration

func TestLiveMongoOperationContract(t *testing.T) {
	config, ok := liveMongoConfigFromEnv(); if !ok { t.Skip("THINKGO_LIVE_MONGO_* 未配置") }
	database := connectLiveDatabase(t, config); defer database.Close()
	id, err := database.Name("bench_orm_users").InsertGetId(map[string]any{"name": "Ada"}); if err != nil { t.Fatal(err) }
	updated, err := database.Name("bench_orm_users").WhereField("_id", "=", fmt.Sprint(id)).UpdateResult(map[string]any{"name": "Ada"})
	if err != nil || !updated.MatchedKnown || !updated.ModifiedKnown { t.Fatalf("Mongo result=%#v %v", updated, err) }
}

func TestLiveNeoStrictAndDetachDelete(t *testing.T) {
	config, ok := liveNeoConfigFromEnv(); if !ok { t.Skip("THINKGO_LIVE_NEO4J_* 未配置") }
	database := connectLiveDatabase(t, config); defer database.Close()
	seedLiveNeoRelatedNodes(t, database, "bench_orm_parent", "bench_orm_child")
	if _, err := database.Name("bench_orm_parent").WhereField("id", "=", "bench_orm_parent").Delete(); err == nil { t.Fatal("strict Delete 应被现存关系拒绝") }
	result, err := database.Name("bench_orm_parent").WhereField("id", "=", "bench_orm_parent").DetachDeleteResult()
	if err != nil || result.Deleted != 1 || !result.RelatedDeletedKnown || result.RelatedDeleted < 1 { t.Fatalf("detach result=%#v err=%v", result, err) }
}

func BenchmarkLiveMongoMaterialization(b *testing.B) {
	config, ok := liveMongoConfigFromEnv(); if !ok { b.Skip("THINKGO_LIVE_MONGO_* 未配置") }
	for _, rows := range []int{10000, 100000} {
		for _, concurrency := range []int{1, 32, 256} {
			b.Run(fmt.Sprintf("rows-%d/concurrency-%d", rows, concurrency), func(b *testing.B) {
				benchmarkLiveMongoSelect(b, config, rows, concurrency)
			})
		}
	}
}

func BenchmarkLiveNeoSessionCollect(b *testing.B) {
	config, ok := liveNeoConfigFromEnv(); if !ok { b.Skip("THINKGO_LIVE_NEO4J_* 未配置") }
	for _, rows := range []int{10000, 100000} {
		for _, concurrency := range []int{1, 32, 256} {
			b.Run(fmt.Sprintf("rows-%d/concurrency-%d", rows, concurrency), func(b *testing.B) {
				benchmarkLiveNeoCollect(b, config, rows, concurrency)
			})
		}
	}
}
```

同一文件内实现上述 helper：每个 helper 先用固定前缀清理并 seed 精确行数，`b.ResetTimer` 后用 semaphore 将 goroutine 数固定为 concurrency；Mongo 分别测 cursor 全量物化、深 skip、已转义 regex，Neo 测每操作 managed session + collect。每轮校验返回行数，cleanup 注册到 `b.Cleanup`，集合/label 名带本进程随机后缀，避免触碰业务数据。

- [ ] **Step 3: 运行基准和 live tag**

Run: `$env:CGO_ENABLED='1'; go test ./framework/db -run '^$' -bench 'Benchmark(RelationIndex|ChunkPaginationSQLite)$' -benchmem -count=5; go test -tags integration ./framework/db -run 'TestLiveMongo|TestLiveNeo' -count=1 -v; go test -tags integration ./framework/db -run '^$' -bench 'BenchmarkLive(Mongo|Neo)' -benchmem -benchtime=3s -count=5`

Expected: SQLite benchmark完成；Mongo/Neo 有服务时 contract 与 12 组 live benchmark（每驱动 2 个数据量 × 3 个并发层级，并展开 Mongo 子场景）完成，无服务时输出明确 SKIP，并在报告矩阵逐项写 SKIP 原因。

- [ ] **Step 4: 记录不应擅自优化的候选**

报告必须分别写出深 OFFSET 与 keyset 数据；`Chunk` API 不自动改为 keyset，因为缺少唯一键契约。Mongo cursor 全量物化、Neo session 吞吐和连接池参数只有 live profile 显示热点时才开生产修改；当前无数据则标记“未合入，等待 live profile”。

- [ ] **Step 5: 提交基准/live 边界**

```powershell
git add framework/db/orm_benchmark_test.go framework/db/live_nosql_integration_test.go docs/数据库/ORM性能报告-2026-07-14.md
git commit -m "test(db): add ORM scale and live driver benchmarks"
```

### Task 5: API 文档与破坏性迁移指南

**Files:**
- Create: `docs/数据库/ORM升级迁移指南.md`
- Modify: `docs/数据库/查询构造器.md`
- Modify: `docs/数据库/模型.md`
- Modify: `docs/数据库/连接数据库.md`
- Modify: `docs/数据库/事务.md`
- Modify: `docs/数据库/MongoDB与Neo4j.md`
- Modify: `docs/数据库/ORM源码审计-2026-07-13.md`
- Modify: `docs/README.md`
- Modify: `README.md` when Step 2 finds an old ORM contract; otherwise leave it untouched and record the zero-match result
- Create: `framework/db/documentation_contract_test.go`

**Interfaces:**
- Produces: 源码可编译示例和 27 项最终状态表。

- [ ] **Step 1: 写文档 API 契约测试**

```go
func TestDocumentedThinkPHPWriteAPICompiles(t *testing.T) {
	var query *Query
	var model *Model
	_ = func() error {
		_, _ = query.Insert(map[string]any{"name": "Ada"})
		_, _ = query.InsertGetId(map[string]any{"name": "Ada"})
		_, _ = query.InsertAll([]map[string]any{{"name": "Ada"}})
		_, _ = query.Save(map[string]any{"name": "Ada"})
		_, _ = query.UpdateResult(map[string]any{"name": "Ada"})
		_, _ = query.DeleteResult()
		return model.Save(&documentationUser{})
	}
}
```

- [ ] **Step 2: 搜索全部旧 API/错误语义**

Run: `rg -n 'Insert.*最后插入|Insert\([^\n]+\).*id|GetConnection|where \[\]string|静默.*锁|DETACH DELETE' README.md docs framework -g '*.md' -g '*.go'`

Expected: 输出待迁移位置清单；逐一分类为源码已删、测试故意断言或文档需要更新。

- [ ] **Step 3: 编写迁移指南和更新全部使用文档**

```go
// 旧：id, err := query.Insert(data)
// 新：
id, err := query.InsertGetId(data)

// 只关心插入数量：
affected, err := query.Insert(data)

// 需要区分 matched / modified：
result, err := query.WhereField("id", "=", 7).UpdateResult(data)
```

迁移指南逐项覆盖 Query CRUD、Model Save/Create、不可变链式调用必须接收返回值、Searcher 新签名、WithConnection、CursorCodec、锁 unsupported、Neo DetachDelete 和 Mongo 主键。所有其他数据库文档使用同一签名和语义。

- [ ] **Step 4: 更新 27 项状态和文档索引**

审计报告每项保留原证据，在其后追加：修复状态、实际 commit、测试名、真实驱动状态。顶部增加 27 项汇总表，状态只能是 `fixed + unit/real driver verified`、`fixed + live-driver pending`；不得出现源码未修复。`docs/README.md` 链接迁移指南、审计和性能报告。

- [ ] **Step 5: 验证并提交文档**

Run: `go test ./framework/db -run TestDocumentedThinkPHPWriteAPICompiles -count=1; rg -n '(password|passwd|pwd|dsn)\s*[:=]\s*[^$` ]' docs/数据库 -g '*.md'; git diff --check`

Expected: 文档契约测试和 diff check PASS；凭据扫描无真实 secret/完整 DSN，只允许字段名、环境变量名和已脱敏示例。随后：

```powershell
git add README.md docs framework/db/documentation_contract_test.go
git commit -m "docs: publish ORM remediation and migration guide"
```

### Task 6: 最终全量验证、独立审查与交付记录

**Files:**
- Modify: `docs/数据库/ORM性能报告-2026-07-14.md`
- Modify: `docs/数据库/ORM源码审计-2026-07-13.md` only if verification status changes

**Interfaces:**
- Consumes: 所有 ORM/CLI commits。
- Produces: 可复现最终验证证据和独立审查结论。

- [ ] **Step 1: 搜索旧契约和配置文件增量**

Run: `rg -n 'ContextualConnection|InsertContextWithPrimaryKey|where \[\]string|func \(q \*Query\) Insert\([^\n]+\).*最后插入' framework/db docs README.md -g '*.go' -g '*.md'; git diff 4ad0875 --name-only | rg '(^|/)(\.air\.toml|.*\.env|config/)'`

Expected: 第一条只有迁移指南中的旧示例说明；第二条无新增配置文件，主工作区 `.env.example` 不在分支 diff。

- [ ] **Step 2: 运行 ORM race、vet、Oracle tag**

Run: `$env:CGO_ENABLED='1'; go test -race ./framework/db/... -count=1; go vet ./framework/db/...; go test -tags oracle ./framework/db/... -count=1`

Expected: 全部 PASS，无 race/vet 输出。

- [ ] **Step 3: 运行 CLI 和全仓测试**

Run: `go test ./framework/console/command -count=1; go test ./... -count=1; git diff --check`

Expected: 全部 PASS。若命中已知 Windows session lock 波动，保存首次错误，运行失败用例 `-count=5`，随后立即复跑 `go test ./... -count=1`；只有定向和全量复跑均 PASS 才记录为既有波动。

- [ ] **Step 4: 使用 requesting-code-review 做阶段和全分支独立审查**

审查输入包含六份规格、五份实施计划、27 项状态表、`git diff 4ad0875...HEAD` 和验证输出。Critical/Important/Minor 必须逐项复现、修复、补测试并复审；最终结论必须为接受或列出仍阻塞交付的问题。

- [ ] **Step 5: 写最终结果并提交**

把真实 SQLite/MySQL 基准、Mongo/Neo live 状态、全部命令、Windows 波动（如发生）、审查结论和未合入优化写入性能报告/审计报告，然后运行：

```powershell
git add docs/数据库/ORM性能报告-2026-07-14.md docs/数据库/ORM源码审计-2026-07-13.md
git commit -m "docs: record final ORM remediation verification"
```
