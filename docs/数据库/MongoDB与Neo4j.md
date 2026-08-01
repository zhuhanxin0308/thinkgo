# MongoDB 与 Neo4j

MongoDB 和 Neo4j 都实现了 `db.Connection`，因此可以使用统一的连接管理、上下文、条件、分页和基础写入接口；它们不是 SQL 数据库，不支持 SQL 事务封装。

请按数据库类型阅读独立章节：

- [MongoDB](MongoDB.md)
- [Neo4j](Neo4j.md)

两者共有的边界：

- 更新和删除都必须包含业务 WHERE。
- 条件值使用参数化转换，集合名、Label、字段和排序都会校验。
- 默认操作有界超时，响应 `WithContext` 的取消和截止时间。
- JOIN、GROUP、HAVING、悲观锁和 `DB.Query`/`DB.Execute` 等 SQL 能力不适用。
- 复杂文档聚合或图查询应在服务/仓储层明确使用对应官方驱动，并自行管理驱动特有的会话和结果资源。
```json
{
    "default": "mongo",
    "connections": {
        "mongo": {
            "type": "mongo",
            "hostname": "127.0.0.1",
            "hostport": "27017",
            "database": "thinkgo",
            "username": "user",
            "password": "secret",
            "params": {
                "authSource": "admin"
            }
        }
    }
}
```

MongoDB URI 由标准 URL 结构构造，用户名、密码、数据库名和参数会被安全转义，并默认追加 `tls=true`。本地环境如需明文连接，必须显式配置字符串参数 `"tls": "false"`；同时配置 `tls`/`ssl`、大小写不同的重复参数、无用户名密码或非法数据库名会返回 `ErrInvalidDatabaseConfig`。

连接阶段使用 10 秒上下文执行 `Ping`，失败时会释放客户端并同时保留探测与关闭错误。每次数据库操作也有 10 秒默认上限，并响应 `WithContext` 传入的取消与截止时间。

## MongoDB 查询

```go
database, err := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
if err != nil {
	return err
}
rows, err := database.Name("users").
	WhereField("status", "=", 1).
	WhereLike("name", "%go%").
	Order("id DESC").
	Page(1, 20).
	Select()
```

支持的条件形式：

| 条件 | MongoDB 转换 |
|---|---|
| `field = ?` | `{field: value}` |
| `field != ?` | `{field: {$ne: value}}` |
| `field > ?` | `{field: {$gt: value}}` |
| `field >= ?` | `{field: {$gte: value}}` |
| `field < ?` | `{field: {$lt: value}}` |
| `field <= ?` | `{field: {$lte: value}}` |
| `field LIKE ?` | 转义后的正则匹配 |
| `field NOT LIKE ?` | `{field: {$not: regex}}` |
| `field IN (...)` | `{field: {$in: values}}` |
| `field NOT IN (...)` | `{field: {$nin: values}}` |
| `field IS NULL` | `{field: nil}` |
| `field IS NOT NULL` | `{field: {$ne: nil}}` |
| `field BETWEEN ? AND ?` | `{field: {$gte: start, $lte: end}}` |

条件可以使用括号组合顶层 `AND` / `OR`；空 `WhereIn` 生成真正恒假的 `$expr`。所有占位符必须恰好消费完，无法解析、参数不足或参数多余都会返回 `ErrInvalidQuery`，不会丢弃条件后退化为空过滤器。同一字段的上下界会合并，无法安全合并的重复字段会保留为 `$and`，不会被 `map` 覆盖。

普通条件值必须是标量。框架会递归解引用指针并拒绝 map、slice、array、普通 struct，避免通过指针或 JSON 文档注入 NoSQL 运算符；`time.Time`、ObjectID 等驱动标量以及 `[]byte` 可正常使用。LIKE 只接受最大 4096 字节的字符串或字节串，模式会先转义再转换通配符。

字段投影、排序和集合名都执行标识符校验，负数 limit/offset 会返回 `ErrInvalidPagination`。显式字段投影未选择 `_id` 时会自动加入 `_id: 0`，返回形状不会额外泄露文档标识符。

MongoDB 只在查询声明的主键路径上提供 ObjectID 对称转换：默认 `_id`（或 `PrimaryKey(...)` 指定的字段）返回十六进制字符串后，该字符串用于等值、`IN`、嵌套 `AND`/`OR`、更新或删除条件时会恢复为 `bson.ObjectID`；非法十六进制主键返回 `ErrInvalidQuery`。普通字符串字段不会因“看起来像 ObjectID”而转换，非主键及嵌套文档中的 ObjectID 也保留 BSON 类型，避免静默改变业务字段。

MongoDB Model 为了保持 ThinkPHP 风格的 `ID` 使用体验，会把未显式配置的逻辑主键 `id` 对称映射到存储键 `_id`：Create 将真实 `InsertedID` 回填到结构体 `ID`，后续 Save、`Where("id", ...)`、Find、Count 和 Delete 都按 `_id` 执行，查询结果只暴露逻辑字段 `id`。如果集合确实使用业务字段 `id` 作为主键，必须显式调用 `PrimaryKey("id")`，并在 Create/Save 前由应用提供非零业务主键；MongoDB 不会生成该字段，零值会在发出 Insert 前返回 `ErrInvalidModel`。如果结构体标签直接使用 `thinkgo:"_id"`，则显式调用 `PrimaryKey("_id")`。

MongoDB 固有 `_id` 不可变：默认映射模式下，Update 数据包含逻辑别名 `id`、存储键 `_id` 或两者同时出现都会在发出 Update 前返回 `ErrInvalidQuery`。逻辑键和存储键同时出现在一次插入或过滤中也会返回 `ErrInvalidQuery`，框架不会猜测覆盖。显式 `PrimaryKey("id")` 选择的真实业务字段不适用 `_id` 别名规则。

```go
type User struct {
    ID   string `thinkgo:"id"`
    Name string `thinkgo:"name"`
}

model := db.NewModel(database, "users")
user := &User{Name: "Ada"}
err := model.Create(user) // user.ID <- MongoDB _id

user.Name = "Grace"
err = model.Save(user) // UPDATE ... WHERE _id = ObjectID(user.ID)
```

## MongoDB 写入

```go
affected, err := database.Name("users").Insert(map[string]interface{}{
	"name": "张三",
})

id, err := database.Name("users").InsertGetId(map[string]interface{}{
	"name": "李四",
})

updated, err := database.Name("users").
	WhereField("status", "=", 1).
	Update(map[string]interface{}{"status": 2})

deleted, err := database.Name("users").
	WhereField("status", "=", 2).
	Delete()
```

MongoDB 的 `Insert` 返回影响文档数，`InsertGetId` 返回驱动的真实 `InsertedID`；框架不会再用 `1` 冒充主键。插入和更新会复制调用方数据；无业务 WHERE 的更新或删除在访问驱动前返回 `ErrUnsafeFullTableMutation`。返回的更新数使用 `ModifiedCount`，删除数使用 `DeletedCount`。

## Neo4j 连接

配置示例：

```json
{
    "default": "neo4j",
    "connections": {
        "neo4j": {
            "type": "neo4j",
            "hostname": "127.0.0.1",
            "hostport": "7687",
            "username": "neo4j",
            "password": "secret",
            "database": "neo4j",
            "params": {
                "scheme": "neo4j+s"
            }
        }
    }
}
```

默认协议是 `neo4j+s`。允许的协议：

```text
neo4j+s neo4j+ssc neo4j bolt+s bolt+ssc bolt
```

其它协议和除 `scheme` 外的参数会被拒绝。`database` 可省略以使用服务端默认数据库；显式配置时会传给每个会话。密码不能脱离用户名配置，无账号密码时使用驱动的 `NoAuth`。

连接阶段先执行 10 秒有界可达性检查；显式配置 `database` 时，还会在目标数据库的只读会话中执行并消费 `RETURN 1 AS thinkgo_probe`，避免数据库不存在、离线或无权限却通过启动健康检查。任一探测失败都会关闭会话和驱动并聚合错误。

每次查询创建独立读/写会话，并通过官方驱动的 `ExecuteRead` / `ExecuteWrite` 托管事务执行。参数会在每次重试回调内克隆，结果也在回调内完整消费，使集群切主和 transient error 可以使用驱动标准重试；会话关闭错误不会被业务错误遮蔽。

## Neo4j 查询

`Name("User")` 中的表名会映射为 Neo4j Label：

```go
rows, err := database.Name("User").
	WhereField("status", "=", 1).
	Field("name,email").
	Order("name ASC").
	Select()
```

Label、属性、投影别名和排序字段都会使用反引号引用；不允许任意点分层级或动态 Cypher 片段。字段投影生成 `RETURN n.field AS alias`，未指定字段时要求驱动返回节点并复制其属性。

Neo4j 支持常见比较、IN、NOT IN、LIKE、NOT LIKE、BETWEEN、NULL 条件，以及括号组合的顶层 AND/OR。LIKE 会转为有长度上限、完全转义的大小写不敏感 Cypher 正则。条件解析和参数消费是严格的，空 IN 恒假，非法条件不会退化为空 WHERE。

## Neo4j 写入

```go
_, err := database.Name("User").Insert(map[string]interface{}{
	"name": "张三",
})

affected, err := database.Name("User").
	WhereField("name", "=", "张三").
	Update(map[string]interface{}{"status": 1})

deleted, err := database.Name("User").
	WhereField("status", "=", 0).
	Delete()

detached, err := database.Name("User").
	WhereField("status", "=", 0).
	DetachDeleteResult()
```

`Delete` 默认使用严格的单条参数化 `MATCH … WHERE … DELETE n`：节点仍有关联时由 Neo4j 拒绝，不会隐式删除关系。只有调用方明确选择 `DetachDelete` / `DetachDeleteResult` 才生成 `DETACH DELETE n`；后者同时返回 `Deleted` 节点数和 `RelatedDeleted` 关系数。两种模式都从同一次执行的统计信息取数，不再使用存在竞态的“先统计、后删除”两次操作。插入与更新通过唯一计数结果返回影响数，点分属性键会在访问驱动前被拒绝，结果类型或结构异常会返回 `ErrInvalidDatabaseRow` / `ErrInvalidAggregateValue`。

MongoDB 与 Neo4j 连接的 `Close` 都是幂等操作，重复调用返回稳定结果。应用托管连接应由 `app.Close()` 统一关闭。

## 限制

- MongoDB 和 Neo4j 当前不支持 SQL 事务封装。
- JOIN、GROUP、HAVING、悲观锁等 SQL 高级能力不适用于这两个连接。
- 原生 SQL 查询接口不适用于 MongoDB 和 Neo4j。
- 复杂图查询或文档聚合需要使用对应官方驱动时，通过解析得到的 `database.WithConnection(func(db.Connection) error)` 在 callback 租约内进行明确的类型断言，并自行处理参数化、context 和结果消费；不得把连接保存到 callback 外，也不得关闭由框架托管的底层连接。
