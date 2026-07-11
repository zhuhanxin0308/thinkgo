# MongoDB 与 Neo4j

ThinkGo 为 MongoDB 和 Neo4j 提供统一 `db.Connection` 适配层，使它们可以通过 `app.DB.Name(...).Where(...).Select()` 这类查询入口使用。由于底层模型不是 SQL，本章说明支持范围和限制。

## MongoDB 连接

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

MongoDB URI 由标准 URL 结构构造，用户名、密码、数据库名和参数会被安全转义。

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

条件值必须是标量。框架会拒绝 map、slice、array、struct 作为普通条件值，避免从 JSON 请求体传入 `{"$ne": null}` 这类 NoSQL 运算符注入。

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

MongoDB 插入返回 `1` 表示成功，不返回自增 ID。

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

其它协议会被拒绝。

## Neo4j 查询

`Name("User")` 中的表名会映射为 Neo4j Label：

```go
rows, err := app.DB.Name("User").
	WhereField("status", "=", 1).
	Field("name,email").
	Order("name ASC").
	Select()
```

字段投影会生成 `RETURN n.field AS field`；未指定字段时返回节点属性。

Neo4j 支持常见比较、IN、NOT IN、LIKE、NOT LIKE、BETWEEN、NULL 条件。LIKE 会转为转义后的 Cypher 正则。

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

删除会先统计命中节点数，再执行 `DETACH DELETE`。

## 限制

- MongoDB 和 Neo4j 当前不支持 SQL 事务封装。
- JOIN、GROUP、HAVING、悲观锁等 SQL 高级能力不适用于这两个连接。
- 原生 SQL 查询接口不适用于 MongoDB 和 Neo4j。
- 复杂图查询或文档聚合建议使用对应官方驱动的原生 API，并自行处理安全边界。
