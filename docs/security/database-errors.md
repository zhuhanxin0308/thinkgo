# 数据库内部错误与对外响应

关联 issue #34。数据库驱动错误属于业务层内部契约，不等于对外响应：查询和事务仍保留原始错误，以便通过 `errors.Is/As`、SQLSTATE 或类型化结果完成取消、回滚、冲突处理和对账。应用把 `err.Error()` 或错误对象直接序列化到 HTTP，才会把 SQL、表名、约束名、参数或凭据带到展示边界。

## 显式转换为公共错误

`db.SanitizeError(err)` 对 nil 返回 nil，对所有非 nil 错误返回固定的 `db.ErrDatabaseOperationFailed`，消息为“数据库操作失败”。转换不尝试解析 SQL、识别驱动或按错误文本分类，不调用输入错误的 `Error`、`Unwrap`、`Is`、`As` 或格式化方法。类型化 nil error 接口也按非 nil 处理。

公共错误不包装、不持有原始原因；因此 `errors.Unwrap/As` 不会从它重新取得驱动错误。仅把 `Error()` 改成固定文本、却仍然 `Unwrap` 原始驱动错误，不是本接口的契约。输入原始错误不会被修改。

以下是应用响应层的函数；调用前，服务层应已使用原始错误处理事务与部分写入。HTTP 状态和业务错误码由应用明确选择，helper 不自动重试、不查询数据库、不输出日志，也不会把所有业务错误统一替换。

```go
func databaseFailure(raw error) *context.Response {
    public := db.SanitizeError(raw)
    if public == nil {
        return nil
    }
    return context.NewResponse().
        Code(http.StatusInternalServerError).
        Json(map[string]string{
            "code":    "database_error",
            "message": public.Error(),
        })
}
```

上例 `context` 指 `github.com/zhuhanxin0308/thinkgo/v3/context`，`db` 指同模块的 `db` 包，`http` 指标准库 `net/http`。完整可执行输出示例见 `db/public_error_example_test.go`。不能在转换后又拼接原始错误、SQL 或绑定参数到响应中；不要把内部错误对象作为 JSON 的另一个字段。需要请求关联标识时，使用可信的服务端请求号，不回显未经校验的输入。

## 先处理内部结果，再决定展示

不要用公共错误决定是否重试或事务是否提交。尤其是 `*db.PartialWriteError`：写入结果可能已经存在，其 `Result` 用于对账，不应因返回了“数据库操作失败”就再次新增。取消和超时也不由本 helper 推断为“数据库没有产生副作用”。`SanitizeError` 只描述展示结果，不改变这些不确定性。

数据库层原有错误身份、日志脱敏和 HTTP 默认异常处理保持不变。本接口是供自定义响应显式使用的转换，不是全局拦截器，不能阻止应用主动返回原始错误，也不是 SQL 注入防护或授权检查。内部日志应使用现有脱敏/结构化错误策略，不应因为响应安全就把凭据写入日志。

## 回归与边界

```sh
go test -race -shuffle=on -count=20 -run '^TestIssue34' ./db
go test -run '^ExampleSanitizeError$' ./db
go test -run '^$' -fuzz '^FuzzSanitizeErrorDoesNotRetainText$' -fuzztime=10s ./db
```

回归覆盖 nil、包装/聚合错误、驱动错误、取消、部分写入、类型化 nil、带危险自定义方法及不可比较动态值的 error；还验证真实 `DB.Name().Insert` 入口保留测试驱动错误身份、转换不重复写入，以及框架 `Response.Send` 输出一致的 JSON。测试驱动与 HTTP recorder 用于确定性边界验证，不冒充真实数据库服务或完整 HTTP 内核部署。

设计依据：OWASP [Error Handling Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Error_Handling_Cheat_Sheet.html) 的内部诊断/通用响应分离；Go [`errors`](https://pkg.go.dev/errors) 的包装、解包及错误身份契约。
