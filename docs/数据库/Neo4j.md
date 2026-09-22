# Neo4j

Neo4j 通过 `db.Connection` 接口接入。业务代码通过 `app.DB()` 取得数据库服务；`DB.Name("User")` 代表 Neo4j Label `User`，查询构造器将安全条件转换为参数化 Cypher。

## 配置

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

允许的协议为 `neo4j+s`、`neo4j+ssc`、`neo4j`、`bolt+s`、`bolt+ssc` 和 `bolt`。`database` 可以省略以使用服务端默认库；显式配置时会传给每个会话。密码不能脱离用户名配置；无账号密码时使用驱动的 `NoAuth`。

连接阶段和每次操作默认使用 10 秒上下文。每次读写都会创建独立会话，完整消费结果并关闭会话；关闭错误不会遮蔽业务错误。

## 查询

```go
database := app.DB()
rows, err := database.Name("User").
	WhereField("status", "=", 1).
	Field("name,email").
	Order("name ASC").
	Select()
```

上例会生成带反引号 Label 和属性的参数化 Cypher。Label、属性、投影别名和排序字段都会引用；Neo4j 标识符不支持点分层级。

```cypher
MATCH (n:`User`)
RETURN n.`name` AS `name`, n.`email` AS `email`
```

支持比较、IN、NOT IN、LIKE、NOT LIKE、BETWEEN、NULL 以及顶层 AND/OR。LIKE 会转换为有长度上限、完全转义且大小写不敏感的 Cypher 正则。集合参数使用 Cypher 参数，不把值拼接进语句。

不指定 `Field` 时要求驱动返回单个节点，并复制节点属性为结果 map；指定投影时别名必须唯一，重复别名或结果列缺失会返回 `ErrInvalidDatabaseRow`。

## 写入

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
```

插入和更新使用参数化属性 map；更新和删除没有业务 WHERE 时返回 `ErrUnsafeFullTableMutation`。删除生成 `DETACH DELETE`，在一次执行结果中读取删除计数，避免“先统计、后删除”的竞态。

## 限制

- Neo4j 不支持 `db.Tx` SQL 事务封装。
- SQL JOIN、GROUP、HAVING、悲观锁和原生 SQL 不适用。
- 复杂图查询应在服务或仓储层明确使用 Neo4j 官方驱动，处理 context、会话、结果消费和关闭。
- `Close` 幂等，应用退出时统一关闭驱动。
