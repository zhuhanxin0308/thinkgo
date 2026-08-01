# MongoDB

MongoDB 通过 `db.Connection` 接口接入，使用 `DB.Name` 的短名称作为集合名。业务代码通过 `framework.ServiceDB` 解析数据库服务。它复用安全条件、分页、写入保护和上下文入口，但不提供 SQL 的 JOIN、GROUP、HAVING、悲观锁或原生 SQL。

## 配置

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

URI 使用标准 URL 构造，凭据、数据库名和参数会转义。默认追加 `tls=true`；本地明文连接必须显式传入字符串参数 `"tls": "false"`。重复的大小写参数、同时配置 `tls`/`ssl`、只配置密码或非法数据库名会返回 `ErrInvalidDatabaseConfig`。

连接阶段使用 10 秒上下文执行 Ping；每次操作默认也有 10 秒上限，并会合并请求上下文的取消和截止时间。

## 查询

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

支持的统一条件包括：

| 条件 | MongoDB 语义 |
|---|---|
| `=`、`!=`、`>`、`>=`、`<`、`<=` | 比较运算符 |
| `LIKE`、`NOT LIKE` | 转义后的正则表达式 |
| `IN`、`NOT IN` | `$in`、`$nin` |
| `IS NULL`、`IS NOT NULL` | nil 或 `$ne: nil` |
| `BETWEEN` | `$gte` 与 `$lte` |
| 顶层 AND/OR | `$and` 与 `$or` |

条件参数必须是标量。框架递归解引用指针，拒绝 map、slice、array 和普通 struct，避免通过条件值注入 MongoDB 运算符；`time.Time`、ObjectID、Decimal128、Binary 和 `[]byte` 等驱动值可用。LIKE 模式最多 4096 字节，并先转义再转换通配符。

字段投影、排序、集合名和分页值均校验。投影使用逗号分隔的字段名，不支持 SQL `AS` 别名；MongoDB 返回的 BSON 文档会递归规范化，ObjectID 转为十六进制字符串，驱动字节缓冲不会直接复用。

## 流式读取与游标分页

`Each` 使用 MongoDB 游标逐文档消费，回调返回 `false` 时提前关闭游标；适合大结果集或只需要处理前缀结果的任务：

```go
err := database.Name("users").
	Field("id,name,status").
	Order("id ASC").
	Each(func(row map[string]interface{}) bool {
		return process(row)
	})
```

`Select` 仍然会返回完整切片；处理大结果集时应优先使用 `Field` 缩小投影，并使用 `Each` 避免业务层长期持有完整结果集。`SeekPage` 和 `ChunkById` 使用主键比较、升序排序和限制读取，MongoDB 中对应比较过滤和游标读取；它们不会执行 `COUNT` 或 `OFFSET`。框架不会自动创建索引，生产集合应根据过滤条件、排序字段和游标字段建立匹配索引，否则数据库仍可能扫描大量文档。

普通 `Page`、`Paginate` 和 `Chunk` 仍使用 OFFSET 语义；深分页的 `skip` 成本不会被 MongoDB 连接自动改写为 keyset。需要稳定性能时请显式使用 `SeekPage` 或 `ChunkById`，并保证游标字段非空、严格递增且在投影中存在。

## 写入

```go
_, err := database.Name("users").Insert(map[string]interface{}{
	"name": "张三",
})

affected, err := database.Name("users").
	WhereField("status", "=", 1).
	Update(map[string]interface{}{"status": 2})

deleted, err := database.Name("users").
	WhereField("status", "=", 2).
	Delete()
```

MongoDB 插入成功返回 `1`，不返回自增 ID。更新返回 `ModifiedCount`，删除返回 `DeletedCount`。插入和更新会复制调用方数据；无业务 WHERE 的更新和删除在访问驱动前返回 `ErrUnsafeFullTableMutation`。

## 限制

- MongoDB 不支持 `db.Tx` SQL 事务封装。
- `Join`、`Group`、`Having`、`Lock` 和 `DB.Query`/`DB.Execute` 不适用。
- 复杂聚合应在服务或仓储层明确使用 MongoDB 官方驱动，并自行处理 context、结果消费和资源关闭。
- `Close` 幂等，应用退出时由应用生命周期统一关闭。
