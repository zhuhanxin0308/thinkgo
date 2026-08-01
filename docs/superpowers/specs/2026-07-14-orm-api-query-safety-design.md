# ORM API、结果契约与查询安全设计

## 1. 范围

本子项目建立其余修复依赖的公共契约，覆盖：

- T3-01：低层结构化 where 实际仍接受任意 SQL；
- T3-02：共享 `*Query` 分支污染和 data race；
- T3-07：普通 SQLConnection 路径拒绝框架生成的聚合表达式；
- NQ-01：NoSQL 插入数量被误当成 ID；
- NQ-08：不同后端 Update 返回 matched/modified 不一致。

## 2. 公共 API 与兼容边界

Query 对外采用总设计中的 ThinkPHP 风格 API：`Save`、`Insert`、`InsertGetId`、`InsertAll`、`Update`、`Delete`。旧 `Insert` 返回主键的语义直接删除，不提供配置开关或运行时兼容。

`UpdateResult` 和 `DeleteResult` 是 Go 端详细结果扩展。简化 `Update` 的稳定语义定义为：优先返回 `Modified`；驱动只能给出一个不区分 matched/modified 的数量时，返回该数量并在详细结果的能力标志中说明。业务若要判断“记录存在但值未变化”，必须调用 `UpdateResult`。

`Delete` 始终只返回目标记录/文档/节点数量；Neo4j 同时删除的关系数量只在 `DeleteResult` 暴露。

## 3. Connection 内部契约

Connection 的写入方法改用结构化 `InsertResult`、`UpdateResult`、`DeleteResult`。SQL、MongoDB、Neo4j 适配器必须明确填写能确认的字段，不能用常量 `1` 伪造 ID，也不能把 matched 偷换成 modified。

SQL 方言返回主键的路径统一进入 `InsertResult.ID`：

- MySQL/SQLite 使用 `LastInsertId`；
- PostgreSQL/SQL Server/Oracle 使用现有 RETURNING/OUTPUT 路径；
- MongoDB 使用 `InsertedID`；
- Neo4j 使用应用主键或明确返回的图身份，不能再返回创建节点数量作为 ID。

当调用 `InsertGetId` 但当前后端/写入没有 ID 时，返回稳定的 `ErrInsertIDUnavailable`，不返回零值假装成功。

## 4. 安全条件树

新增或收紧内部 Condition AST，至少区分：

- 字段比较、空值、集合、区间；
- AND/OR/NOT 组合；
- 框架生成的聚合/字段表达式；
- 显式 Raw 节点。

普通 `Where`、`Having`、Model scope 和关系条件只能生成受验证节点和值参数。任意 SQL 片段只能通过名字明确的 `WhereRaw`/`HavingRaw`/Raw expression 创建，Raw 类型不能由普通字符串隐式转换。

SQLConnection 的公开 CRUD 不再接收 `where []string`。Builder 只编译 AST 或带 provenance 的内部编译产物。占位符计数仍保留，但它只是 Raw API 的完整性检查，不再被当作注入防护。

## 5. Query 不可变语义

所有链式方法采用 copy-on-write：调用 `Where`、`Order`、`Limit`、`With`、`Lock` 等方法返回的新 Query 拥有独立状态，原 Query 不被修改。

实现必须深拷贝可变容器：条件树、字段切片、join、order、group、having、relation/preload、data map 和 nested slice/map。只复制顶层结构体不足以满足要求。

并发契约：一个基础 Query 可以安全地被多个 goroutine 派生和执行；同一个 Query 实例的执行过程不写回查询状态。连接池和 logger 等只读共享依赖可共享。

## 6. 聚合表达式

`Count/Sum/Avg/Min/Max` 不再先拼字符串再经过 plain identifier 校验。框架生成 typed aggregate expression，由 Builder 按方言引用字段和别名。

用户输入只允许受验证的字段名；函数名来自固定枚举；别名由框架固定。Raw aggregate 必须走显式 Raw API。普通和事务/高级查询路径复用同一编译器，避免同一 API 因执行路径不同而成败不一。

## 7. 错误处理

- 无法返回插入 ID：`ErrInsertIDUnavailable`；
- 普通 API 收到 Raw/不安全来源：`ErrUnsafeExpression`；
- 驱动不支持详细计数时不报假错误，但用 `Known` 标志表达能力；
- 所有溢出、负数或互相矛盾的驱动计数返回结果校验错误。

错误使用 `errors.Is/As` 可判断，驱动原始错误保留在错误链中；日志安全由生命周期子项目处理。

## 8. TDD 与验收

必须先增加以下失败测试：

- SQL、MongoDB、Neo4j 的 `Insert` 返回数量，`InsertGetId` 返回真实且保留类型的 ID；
- 无 ID 后端调用 `InsertGetId` 返回稳定错误；
- matched=3/modified=2 时简化和详细 Update 契约一致；
- 低层 CRUD 无法注入来源不明的 where 字符串，显式 Raw 仍可用；
- 从同一基础 Query 派生两个分支互不污染，并通过 `-race` 并发测试；
- CGO SQLite 普通路径的五种聚合均成功且数值正确；
- 普通路径与 transaction/advanced path 生成等价 SQL/参数。

本阶段结束时仓库内旧写入签名和旧 `Insert` 主键假设必须清零，所有调用点可编译。
