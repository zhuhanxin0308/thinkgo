# 原生 SQL

服务层应优先使用 ORM。只有 JOIN/聚合/窗口/数据库专有能力等复杂查询无法由安全查询构造器清晰表达时，才使用原生 SQL。

## 查询与执行

```go
database := app.DB()
rows, err := database.Query(
	"SELECT id, name FROM users WHERE status = ?",
	1,
)

affected, err := database.Execute(
	"UPDATE users SET status = ? WHERE id = ?",
	2,
	userID,
)
```

`Query` 返回 `[]map[string]interface{}`；`Execute` 返回影响行数。请求内使用 `QueryContext` 和 `ExecuteContext` 传递超时和取消信号。

## 占位符规则

框架约定原生 SQL 统一使用 `?` 占位符，执行前由当前方言重绑定：

| 方言 | 底层占位符 |
|---|---|
| MySQL、SQLite | `?` |
| PostgreSQL | `$1`、`$2` |
| SQL Server | `@p1`、`@p2` |
| Oracle | `:1`、`:2` |

执行前框架会扫描 SQL 并严格检查占位符数量。扫描会忽略字符串、双引号/反引号/方括号标识符、行注释、块注释和 PostgreSQL dollar quote 中的问号。未闭合引用、参数不足或多余参数都会返回 `ErrInvalidQuery`。

`Query`、`Execute` 及底层 `SQLConnection` 还会检查当前方言的绑定参数预算；超出时返回 `ErrQueryArgumentsTooMany`，不会把超大参数列表交给驱动。大批量写入请使用 `InsertAll` 自动分批，读取大集合请按批次拆分条件。

PostgreSQL JSONB 的单字符问号操作符需要写成 `??`：

```go
rows, err := database.Query("SELECT * FROM documents WHERE payload ?? ?", "role")
```

`?|`、`?&` 和 `@?` 按 PostgreSQL 操作符处理，不会误认为绑定参数。

## 原生条件

查询构造器提供 `WhereRaw` 和 `HavingRaw`：

```go
q.WhereRaw("JSON_EXTRACT(profile, '$.role') = ?", "admin")
q.HavingRaw("SUM(amount) > ?", 1000)
```

这些方法只校验非空、NUL 字节和参数数量，不会解析完整原生表达式。原生片段必须来自代码或可信的固定模板；用户输入只能作为参数绑定。

## 事务中的原生入口

`tx.Execute` 和 `tx.ExecuteContext` 为事务内的高阶写入保留一个收窄入口，只允许单条参数化 `INSERT` 或 UPSERT：

```go
err := database.Transaction(func(tx *db.Tx) error {
	_, err := tx.Execute(
		"INSERT INTO audit_events (event_id, action) VALUES (?, ?)",
		eventID,
		"login",
	)
	return err
})
```

事务原生入口拒绝多语句、UPDATE 和 DELETE。事务中的更新、删除应使用 `tx.Name` 或 `tx.Table` 的结构化 ORM，继承无 WHERE 全表写保护。

## 表名前缀

原生 SQL 不会自动给 SQL 文本中的表名追加前缀。表名必须先从代码白名单选择，再通过 `ResolveTableName` 获取完整名称；不要把请求参数直接拼进 SQL。通常更推荐使用 `Name`、`Table` 和 ORM 生成 SQL，避免手动处理引用和前缀。

## 日志脱敏

数据库错误日志只保留操作类型、表名、脱敏后的 SQL 结构和参数数量，不记录绑定参数值。SQL 中的字符串字面量、注释和敏感赋值会被替换为脱敏标记。业务日志仍不得主动记录密码、令牌和个人信息。
