# Recovery

`framework/middleware.Recovery` 捕获下游处理过程中的 panic，并把它交给 `framework/exception.Handle` 渲染为统一的 `context.Response`。应用初始化时它始终位于全局管道最前面。

## 装配

Recovery 由框架自动放在全局管道最前面。业务项目只需要在 `app/exception_handle.go` 定制异常的 `Report` 与 `Render`，不需要创建 Recovery、解析日志服务或修改 HTTP 内核。

默认处理器通过 `app.Log()` 记录异常，调试模板目录由应用路径 API 确定。不要在请求之间保存异常对象或修改共享恢复器状态。

## 响应行为

- 下游返回非空响应时原样返回。
- 下游返回 `nil` 时返回空的框架响应，避免全局管道中出现空指针。
- 下游 panic 时使用 `httptest.ResponseRecorder` 暂存标准异常渲染结果，再转换为 `context.Response`，保留状态码、响应头和响应体。
- HTTP 异常、业务异常、验证异常和普通 panic 的状态码与消息由异常处理器决定；调试详情必须同时满足调试模式和本机回环请求门禁。
- 异常渲染失败会写日志，并继续返回已经转换的响应；请求体解析位于 Recovery 下游，HTTP 内核最外层恢复逻辑负责请求对象创建、`HttpRun`、响应发送或 Recovery 自身边界之外的 panic。

Recovery 不会把 panic 重新抛给下游，也不会在响应已经写出后尝试拼接新的响应体。异常响应仍会经过 HTTP 内核的压缩关闭、terminate、访问日志和 `HttpEnd` 收尾流程。
