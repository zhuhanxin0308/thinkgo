# LIKE 模式与用户字面文本

关联 issue #45。`WhereLike` 的参数是有意保留通配语义的模式：`%` 表示任意长度，`_` 表示单个字符；SQL Server 还具有方括号模式。值绑定防止它变成 SQL 语法，不会自动去掉通配符。现有 MongoDB、Neo4j 适配也把 LIKE 的 `%`/`_` 转换为正则通配，不能把使用 `QuoteMeta` 理解为已经逐字匹配。

## 显式选择字面匹配

新增 `Query.WhereLikeLiteral(field, literal)` 与 `ModelQuery.WhereLikeLiteral(field, literal)`。例如已经取得可信的租户身份后：

```go
rows, err := database.Name("users").
    Where("tenant_id", authenticatedTenantID).
    WhereLikeLiteral("display_name", userInput).
    Select()
```

输入 `100%_done` 只作为完整字面文本匹配，不会将 `%`/`_` 解释成通配符，也不会自动在前后追加 `%`。需要有意模式搜索时继续使用 `WhereLike`；确实需要精确等值查询时也可按业务字段使用 `Where`。本接口不是 contains、前缀或后缀搜索。

“字面”只消除模式元字符的能力，不统一数据库排序规则、大小写、Unicode 等价性或 NULL 行为。尤其 Oracle 将空字符串作为 NULL 处理；不要据此推导所有后端的空文本查询完全一致。鉴权必须使用已验证的身份与对象范围，不能把 LIKE 搜索当作授权。接口沿用查询不可变性和现有布尔分组规则，不扩大或替换调用方的授权条件。

字段名经过既有标识符校验，值始终作为参数绑定。非法 UTF-8 和 NUL 文本返回 `ErrInvalidQuery`。MongoDB、Neo4j 仍执行各自已有的 LIKE 文本长度限制；未支持的 SQL 方言返回 `ErrUnsupportedFeature`，不会自行猜测转义规则。

## 方言与适配实现

SQL 路径同时生成转义后的绑定值与显式 `ESCAPE '!'` 子句：转义 `!`、`%`、`_`，仅 SQL Server 额外转义 `[`。不会把 SQL Server 的方括号转义带到 Oracle；反斜线在显式 `!` 规则下按普通文本处理。条件树保留原始文本，因此切换实际编译方言、生成 Clauses 快照或重复编译不会二次转义。

MongoDB 与 Neo4j 消费结构化的 `PredicateLikeLiteral` 节点，将文本逐字引用后用绝对起止锚点包围，避免字符串末尾换行被普通 `$` 锚点忽略。它们保留既有 LIKE 适配的忽略大小写行为。节点值不会被拼接进 SQL 或 Cypher 语句；兼容 SQL 快照不替代原生节点编译。

第三方驱动如消费 `Predicate.Nodes/PortableNodes`，需要明确支持新节点或拒绝该能力。既有 `WhereLike`、Raw、关系、写入返回值和 Builder 公开接口均未修改。

## 验证边界

`db/like_literal_example_test.go` 提供无连接的可执行 SQL 编译示例。可移植测试覆盖四种默认 SQL 方言、方言重编译、节点快照、字段/文本拒绝、模型包装及两个原生编译器；`oracle` 构建标签增加 Oracle 编译检查，但不是实际 Oracle 服务测试。

启用 CGO 的 SQLite 测试检查真实行集合：百分号、下划线、方括号、反斜线、引号、Unicode 和末尾换行不能扩大匹配；独立 score 范围内的同名记录不被带入或修改。相同断言接入已有 MySQL/PostgreSQL 与 MongoDB/Neo4j 的真实服务测试入口；本地缺少这些服务时不把编译通过或跳过当成运行成功。

```sh
go test -race -shuffle=on -count=20 -run '^(TestLiteralLike|ExampleQuery_WhereLikeLiteral)' ./db ./db/driver/mongo ./db/driver/neo4j
go test -tags=oracle -run '^TestLiteralLikeOracle$' ./db
go test -run '^$' -fuzz '^FuzzLiteralLikeEscapeRoundTrip$' -fuzztime=10s ./db
```

把新接口故意退化为原有 `WhereLike` 的负对照会在 SQLite 的 `%` 样本中匹配17行而不是1行。该负对照证明测试能识别通配回退；它不是声称此前不存在的 API 已有实现缺陷，更不是对旧 `WhereLike` 模式功能的否定。

参考方言规则：PostgreSQL `functions-matching`、MySQL `string-comparison-functions`、SQL Server `LIKE (Transact-SQL)`、Oracle `Pattern-matching Conditions`。实际大小写及排序行为仍由所选后端决定。
