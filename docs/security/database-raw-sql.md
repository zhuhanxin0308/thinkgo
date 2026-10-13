# Raw SQL：固定结构、绑定值与查询快照

`WhereRaw`、`HavingRaw`、`DB.Query` / `DB.Execute` 及其 Context、事务入口是可信 SQL 通道。空文本、NUL、引用闭合和参数数量检查不判断 SQL 是否符合业务授权，也不能把拼接了用户输入的 SQL 变安全。不要把存在 Raw API 本身等同于已发现框架注入漏洞。

## 值必须与 SQL 分开

下面的反例只用于解释错误，不能放入业务代码：

```go
// 错误：submittedName 的引号和运算符会成为 SQL 结构。
statement := "SELECT id FROM users WHERE name = '" + submittedName + "'"
```

正确的调用片段，假设 `database` 已初始化、`ctx` 为调用上下文、`tenantID` 来自已认证身份：

```go
rows, err := database.QueryContext(ctx,
    "SELECT id FROM users WHERE tenant_id = ? AND name = ?",
    tenantID, submittedName,
)
if err != nil {
    return err // 在公共响应边界转换错误，不直接回显。
}
_ = rows
```

不要给 `?` 加引号，也不要先手动转义再绑定。框架原生 SQL 入口统一使用 `?`，SQL 适配层按方言重绑定；不要在同一调用混用 `$1`、`@p1` 等原生编号。PostgreSQL 问号操作符按现有规则处理：单独的 JSONB `?` 写成 `??`，`?|`、`?&`、`@?` 保留。直接调用底层 `*sql.DB` 时，则遵守所选驱动自己的占位符协议。

链式接口采用同一原则：

```go
rows, err := database.Table("users").WithContext(ctx).
    WhereField("tenant_id", "=", tenantID).
    WhereRaw("name = ?", submittedName).
    Select()
```

`HavingRaw("SUM(amount) > ?", minimum)` 中聚合结构由业务固定，`minimum` 仍作为值绑定。更新同样使用 `ExecuteContext(ctx, "UPDATE users SET nickname = ? WHERE tenant_id = ? AND id = ?", nickname, tenantID, userID)`。处理 SQL 错误或超时不能据此假定写入没有发生；事务和部分写入语义见 [插入结果](database-insert-results.md)。

## 标识符与业务范围不能由绑定替代

`ORDER BY ?` 不能把用户输入安全地变成列名。用业务允许列表把公开选项映射到固定结构，例如 `"newest" -> "created_at DESC, id DESC"`、`"oldest" -> "created_at ASC, id ASC"`；未命中直接返回参数错误，再把受信结果传给 `Order`。字段语法合法不代表调用者有权选择该字段。

租户或对象条件由身份认证结果产生，不能从另一个未授权请求字段取得。绑定防止值变为 SQL 代码，不阻止调用者删除授权条件、选择别人的 ID，或在可信 SQL 中编写过宽的 OR。对于用户输入的字面搜索，使用 [WhereLikeLiteral](database-literal-like.md)；普通 LIKE 的 `%` 和 `_` 即使参数化仍然是通配符。

## 构建结果与可复用查询

`BuildSelectSQL` 返回 SQL 和参数，不执行数据库操作。外层参数切片及按已有快照规则支持的可变容器与查询内部隔离；修改返回的字节切片、嵌套容器或指针内容，不会影响下一次构建或原查询执行。WHERE 和 HAVING 参数遵守相同规则，参数顺序和 nil/空切片区别保留。

这是既有数据库容器快照机制的返回边界修复，不是对循环对象图、自定义 `driver.Valuer`、带隐藏引用的结构体、函数或资源句柄的新深拷贝承诺。业务仍应传递所选驱动支持的参数类型，保持特殊对象不可变；构造查询期间不得并发修改输入。输出快照可能包含秘密，不应因它已复制就完整写入日志。

## 不采用关键词黑名单作为注入修复

禁用 `sleep`、`benchmark` 或所有分号无法证明剩余 SQL 没有注入，且会误伤合法表达式、字符串和迁移。Raw 通道仍允许可信原生语句，不新增统一关键字或多语句拦截策略。需要受限查询语言时，必须独立设计并审查其语法、方言与允许的操作，而不是把字符串过滤当安全边界。

参考 [Go：避免 SQL 注入](https://go.dev/doc/database/sql-injection) 与 [OWASP SQL Injection Prevention](https://cheatsheetseries.owasp.org/cheatsheets/SQL_Injection_Prevention_Cheat_Sheet.html)。数据库账户仍应最小授权，应用负责认证、对象授权、请求限额和事务结果处理。

## 回归证据范围

普通包测试包含四个默认 SQL 方言的构建/参数分离、可变参数快照与并发输出修改测试及一个外部示例。启用 CGO 时运行真实 SQLite 行集合、更新、取消和事务回滚断言；`integration` 标签下增加 MySQL/PostgreSQL 同一契约，使用既有真实服务配置。未配置远程服务时的 skip 不是远程验证成功；Oracle 编译或其他后端未包含在这四方言默认矩阵中。
