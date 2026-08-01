# ORM 全面修复总设计

## 1. 背景与目标

`docs/数据库/ORM源码审计-2026-07-13.md` 已确认 27 项问题：P1 12 项、P2 12 项、P3 3 项。本文把这些诊断结论转化为一次不保留旧错误语义的 ORM 升级，并以 ThinkPHP 8 所使用的 ThinkORM 3/4 为首要 API 参照。

本次交付同时完成四件事：

1. 修复全部已确认的逻辑、安全、生命周期和驱动一致性问题；
2. 把 Query/Model 写入 API 调整为尽可能接近 ThinkPHP 的命名和语义；
3. 用显式能力、错误或详细结果表达 Go 与不同数据库无法隐藏的差异；
4. 更新数据库相关使用文档、API 文档、迁移指南和审计状态，不让源码与文档脱节。

## 2. ThinkPHP 参考基线

ThinkPHP 8 的官方应用骨架允许使用 ThinkORM 3.x 或 4.x。本设计以官方 4.0 源码为主、3.0 行为为兼容核对，参考点如下：

- [ThinkPHP 8.x 对 ThinkORM 的版本约束](https://github.com/top-think/think/blob/8.x/composer.json#L24-L27)；
- [ThinkORM Query 的 insert、insertGetId、insertAll、update、delete](https://github.com/top-think/think-orm/blob/4.0/src/db/BaseQuery.php#L1321-L1471)；
- [ThinkORM Model 保存和主键回填](https://github.com/top-think/think-orm/blob/4.0/src/Model.php#L353-L425)；
- [ThinkORM Mongo 插入数量与插入 ID 分离](https://github.com/top-think/think-orm/blob/4.0/src/db/connector/Mongo.php#L736-L807)；
- [ThinkORM Mongo 主键 ObjectID 对称转换](https://github.com/top-think/think-orm/blob/4.0/src/db/builder/Mongo.php#L61-L93)。

对齐原则是 API 名称、常用返回语义和 Model 行为尽可能一致，而不是照搬 PHP 的动态类型。Go 无法安全隐藏的差异通过 `any` 主键、typed result、capability error 和明确文档表达。

## 3. 已确认约束

- 采用一步到位的破坏性升级，不保留语义错误的旧 API 兼容层。
- ThinkPHP 可以直接映射的能力优先采用相同命名和语义；Go 必须表达的类型或驱动差异使用额外强类型 API。
- 不增加配置文件，不为 Air、数据库或测试新增仓库级配置文件。
- 不修改主工作区中用户已有的 `.env.example` 变更。
- 真实服务不可用时必须明确记录 live-driver/live-server 验证边界，不能用 mock 结果冒充真实驱动通过。
- 性能优化必须先有基准和 profile，未证明稳定收益的候选不进入生产代码。

## 4. ThinkPHP 风格公共 API

Query 写入 API 统一为：

```go
Save(data map[string]any, forceInsert ...bool) (int64, error)
Insert(data map[string]any) (int64, error)
InsertGetId(data map[string]any) (any, error)
InsertAll(rows []map[string]any) (int64, error)
Update(data map[string]any) (int64, error)
Delete() (int64, error)
```

语义约束：

- `Insert` 返回插入行数，不再返回主键；
- `InsertGetId` 返回驱动生成的真实主键，保留 `int64`、字符串或 NoSQL ID 等实际类型；
- `InsertAll`、`Update`、`Delete` 返回影响数量；
- `Save` 与 ThinkPHP 一致，根据查询条件和 `forceInsert` 选择新增或更新；
- Go 方法名采用用户确认的 `InsertGetId`，保持 ThinkPHP 拼写，而不是改成 `InsertGetID`。

为避免把跨驱动差异继续藏进一个 `int64`，增加 ThinkGo 扩展：

```go
UpdateResult(data map[string]any) (UpdateResult, error)
DeleteResult() (DeleteResult, error)
```

`UpdateResult` 至少显式提供 `Matched` 与 `Modified`，并通过有效性标记说明驱动是否能精确提供两者；`DeleteResult` 至少提供目标对象删除数，并允许图数据库表达同时删除的关系数。简化 API 的 `Update` 和 `Delete` 使用文档定义的稳定投影，不再随驱动暗中改变含义。

Model API 统一为：

```go
Save(v any) error
Create(v any) error
Update(v any) error
Delete() error
ForceDelete() error
Restore() error
```

`Model.Save` 依据主键零值选择 `Create` 或 `Update`；`Create` 使用 `InsertGetId` 并把真实主键写回模型。所有主键可写性和类型兼容性检查必须发生在写数据库之前。

## 5. 内部写入结果模型

Connection/driver 层不再用含义模糊的单个 `int64` 同时承载行数和 ID，而是返回内部结构化结果：

```go
type InsertResult struct {
    Affected int64
    ID       any
    HasID    bool
}

type UpdateResult struct {
    Matched       int64
    Modified      int64
    MatchedKnown  bool
    ModifiedKnown bool
}

type DeleteResult struct {
    Deleted        int64
    RelatedDeleted int64
}
```

具体字段可在实施计划中按 Go 命名规范微调，但必须保留三个不可丢失的事实：插入数量与 ID 分离、匹配数与修改数分离、图删除副作用可见。Query 层再把结构化结果投影到 ThinkPHP 风格的简化返回值。

## 6. 统一数据流

```text
ThinkPHP 风格 Query API
        ↓
不可变 Query 快照 + 安全条件树
        ↓
方言 Builder / Compiler
        ↓
Connection / Driver
        ↓
结构化内部结果
        ↓
Query 简化返回值或详细结果
        ↓
Model 主键回填、事件与关系处理
```

条件、字段、排序和锁都必须在结构化阶段保留来源信息。普通 API 只能产生受验证的 AST；任意 SQL 只能通过显式 `WhereRaw`/Raw API 进入，并由调用方承担信任边界。低层 SQLConnection 不再接收来源不明的 `where []string`。

## 7. 子项目与问题映射

### A. API、结果契约与查询安全

规格：`2026-07-14-orm-api-query-safety-design.md`

覆盖 T3-01、T3-02、T3-07、NQ-01、NQ-08，并为其余子项目建立结构化结果和不可变 Query 基础。

### B. 生命周期、所有权与日志安全

规格：`2026-07-14-orm-lifecycle-logging-design.md`

覆盖 T2-01、T2-02、T2-03、T2-04、T2-05、T2-06。

### C. SQL 方言与连接器一致性

规格：`2026-07-14-orm-sql-dialects-connectors-design.md`

覆盖 T3-03、T3-04、T3-05、T3-06、C-01、C-02、C-03。

### D. Model、关系、MongoDB 与 Neo4j

规格：`2026-07-14-orm-model-nosql-design.md`

覆盖 M-01、M-02、M-03、NQ-02、NQ-03、NQ-04、NQ-05、NQ-06、NQ-07。

### E. 性能验证与文档交付

规格：`2026-07-14-orm-performance-docs-design.md`

覆盖审计报告中所有 benchmark-only 候选、真实数据库验证边界以及最终文档同步。

五个子项目按 A → B → C → D → E 顺序实施。A 提供后续共同依赖；B/C/D 每阶段独立测试和审查；E 只合入已证明的性能优化，并完成总体验收。

## 8. 迁移策略

- 实施过程中允许在同一分支短暂存在新旧内部接口，以保持每个小提交可编译；对应阶段结束时必须删除旧接口及兼容分支。
- 不提供把旧 `Insert` 返回值继续解释成主键的开关或适配器；调用方必须迁移到 `InsertGetId`。
- 旧低层裸连接和原始 where 入口若无法建立明确所有权或来源边界，则直接删除或替换为 lease/typed API。
- 编译错误被视为迁移提示：仓库内全部调用点、示例和文档在同一交付中完成更新。

ThinkPHP 到 ThinkGo 的核心映射：

| ThinkPHP / ThinkORM | ThinkGo | 说明 |
|---|---|---|
| `insert` | `Insert` | 返回插入数量 |
| `insertGetId` | `InsertGetId` | 返回真实主键 |
| `insertAll` | `InsertAll` | 返回插入数量 |
| `save` | `Save` | Query 根据条件选择，Model 根据主键选择 |
| `update` | `Update` | 返回稳定的简化数量 |
| `delete` | `Delete` | 返回目标对象删除数量 |
| 无直接动态类型对应 | `UpdateResult` / `DeleteResult` | ThinkGo 的跨驱动详细结果扩展 |

## 9. 测试与验证总策略

每个问题遵循 TDD：先增加能稳定复现审计问题的失败测试，再做最小修复，最后运行同包回归。不得先改生产代码再补一个只会通过的测试。

验证分层：

1. 纯单元测试固定 API、AST、Builder、状态机、脱敏和事件契约；
2. `CGO_ENABLED=1` 的真实 SQLite 测试覆盖事务、聚合、内存库和写入语义；
3. 本机真实 MySQL 优先覆盖 DSN、返回值、no-op update、并发和性能；
4. PostgreSQL、SQL Server、Oracle、MongoDB、Neo4j 在服务可用时运行集成测试；不可用时保留可发现的 integration/tag 测试并记录边界；
5. Oracle 至少完成 tagged compile 和 builder 契约测试；
6. 每阶段运行 ORM race 与 vet，最终运行全仓测试和独立代码审查。

最终最少执行：

```powershell
$env:CGO_ENABLED='1'
go test -race ./framework/db/... -count=1
go vet ./framework/db/...
go test ./... -count=1
git diff --check
```

如果全仓测试命中已知 Windows 跨进程 session 文件锁波动，必须记录首次失败原文、定向重复结果和紧接着的全量复跑结果，不能直接忽略。

## 10. 文档交付

修复完成后至少同步更新：

- `docs/数据库/查询构造器.md`：新 Query API、返回值、Raw 边界、锁和驱动差异；
- `docs/数据库/模型.md`：`Save/Create` 选择、主键回填、事件数据和软删除；
- `docs/数据库/连接数据库.md`：连接 lease、连接器验证、驱动能力；
- `docs/数据库/事务.md`：context 自动回滚和终态语义；
- `docs/数据库/MongoDB与Neo4j.md`：ObjectID、投影、严格/DETACH 删除、托管事务；
- 新增 ORM 破坏性升级迁移指南；
- `docs/数据库/ORM源码审计-2026-07-13.md`：逐项增加“已修复/测试证据/live-driver 边界”状态，保留原始审计证据和问题描述。

文档中的示例必须参与编译或由契约测试覆盖；不存在的 API、隐含的驱动一致性和未验证的服务端结论不得写成已支持。

## 11. 完成标准

- 27 项 finding 均有对应修复提交与回归测试；只能把真实服务端复核标记为验证边界，不能把源码缺陷留作边界。
- ThinkPHP 风格写入 API、源码、示例和文档一致，旧错误语义已移除。
- 不支持或无法等价的数据库能力返回明确错误，不再静默忽略。
- race、vet、全仓测试和 `git diff --check` 通过，已知外部波动有完整复跑证据。
- 每个阶段有独立代码审查，最终有全分支审查。
- 未新增配置文件，主工作区 `.env.example` 未被触碰。
- 性能优化有修改前后真实基准；没有稳定收益的实验不合入。
