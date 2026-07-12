# MongoDB 与 Neo4j

ThinkGo 为 MongoDB 和 Neo4j 提供统一 `db.Connection` 适配层，使它们可以通过 `app.DB.Name(...).Where(...).Select()` 这类查询入口使用。由于底层模型不是 SQL，本章说明支持范围和限制。

## MongoDB 连接

内置适配器使用 `go.mongodb.org/mongo-driver/v2` v2.8.0，要求 MongoDB Server 4.2 或更高版本。框架的 `db.Connection` 查询与写入 API 保持不变；直接访问 `db.MongoConnection.Client` 的代码必须改用 `/v2/mongo` 类型，ObjectID、DateTime、Decimal128 和 Binary 等 BSON 类型统一从 `/v2/bson` 导入，不再使用 v1 的 `bson/primitive` 包。

配置示例：

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
rows, err := app.DB.Name("users").
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

字段投影、排序和集合名都执行标识符校验，负数 limit/offset 会返回 `ErrInvalidPagination`。查询结果递归规范化嵌套 BSON，ObjectID 返回十六进制字符串，字节切片不会复用驱动缓冲区。

## MongoDB 写入

```go
id, err := app.DB.Name("users").Insert(map[string]interface{}{
	"name": "张三",
})

affected, err := app.DB.Name("users").
	WhereField("status", "=", 1).
	Update(map[string]interface{}{"status": 2})

deleted, err := app.DB.Name("users").
	WhereField("status", "=", 2).
	Delete()
```

MongoDB 插入返回 `1` 表示成功，不返回自增 ID。插入和更新会复制调用方数据；无业务 WHERE 的更新或删除在访问驱动前返回 `ErrUnsafeFullTableMutation`。返回的更新数使用 `ModifiedCount`，删除数使用 `DeletedCount`。

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

连接阶段执行 10 秒有界可达性检查，失败时关闭驱动并聚合错误。每次查询创建独立读/写会话，完整消费结果并关闭会话；会话关闭错误不会被业务错误遮蔽。

## Neo4j 查询

`Name("User")` 中的表名会映射为 Neo4j Label：

```go
rows, err := app.DB.Name("User").
	WhereField("status", "=", 1).
	Field("name,email").
	Order("name ASC").
	Select()
```

Label、属性、投影别名和排序字段都会使用反引号引用；不允许任意点分层级或动态 Cypher 片段。字段投影生成 `RETURN n.field AS alias`，未指定字段时要求驱动返回节点并复制其属性。

Neo4j 支持常见比较、IN、NOT IN、LIKE、NOT LIKE、BETWEEN、NULL 条件，以及括号组合的顶层 AND/OR。LIKE 会转为有长度上限、完全转义的大小写不敏感 Cypher 正则。条件解析和参数消费是严格的，空 IN 恒假，非法条件不会退化为空 WHERE。

## Neo4j 写入

```go
_, err := app.DB.Name("User").Insert(map[string]interface{}{
	"name": "张三",
})

affected, err := app.DB.Name("User").
	WhereField("name", "=", "张三").
	Update(map[string]interface{}{"status": 1})

deleted, err := app.DB.Name("User").
	WhereField("status", "=", 0).
	Delete()
```

删除使用单条参数化 `MATCH … WHERE … DETACH DELETE n`，再从同一执行结果的统计信息读取删除节点数，不再使用存在竞态的“先统计、后删除”两次操作。插入与更新通过唯一计数结果返回影响数，结果类型或结构异常会返回 `ErrInvalidDatabaseRow` / `ErrInvalidAggregateValue`。

MongoDB 与 Neo4j 连接的 `Close` 都是幂等操作，重复调用返回稳定结果。应用托管连接应由 `app.Close()` 统一关闭。

## 限制

- MongoDB 和 Neo4j 当前不支持 SQL 事务封装。
- JOIN、GROUP、HAVING、悲观锁等 SQL 高级能力不适用于这两个连接。
- 原生 SQL 查询接口不适用于 MongoDB 和 Neo4j。
- 复杂图查询或文档聚合需要使用对应官方驱动时，先处理 `app.DB.GetConnection()` 返回的 `(db.Connection, error)`，再进行明确的类型断言，并自行处理参数化、context、结果消费和资源关闭边界。
