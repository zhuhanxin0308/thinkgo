# Recovery

`framework/middleware.Recovery` 捕获下游处理过程中的 panic，并把它交给 `framework/exception.Handle` 渲染为统一的 `context.Response`。应用初始化时它始终位于全局管道最前面。

## 装配

```go
recovery := &middleware.Recovery{
    App:    app,
    Log:    logger,
    TplDir: app.BasePath + "/framework/exception/tpl",
}
pipeline, err := framework.ResolveServiceAs[*middleware.Pipeline](app, framework.ServiceMiddleware)
if err != nil {
    return err
}
if err := pipeline.Pipe(recovery.Handle); err != nil {
    return err
}
```

`logger` 应由 `framework.ResolveServiceAs[*log.Log](app, framework.ServiceLog)` 获取。`App` 只要求实现异常层的 `IsDebug` 契约，`Log` 用于记录恢复渲染失败，`TplDir` 是调试异常页模板目录。推荐使用框架初始化时的装配方式，不要在多个请求之间创建或修改恢复器状态。

## 响应行为

- 下游返回非空响应时原样返回。
- 下游返回 `nil` 时返回空的框架响应，避免全局管道中出现空指针。
- 下游 panic 时使用 `httptest.ResponseRecorder` 暂存标准异常渲染结果，再转换为 `context.Response`，保留状态码、响应头和响应体。
- HTTP 异常、业务异常、验证异常和普通 panic 的状态码与消息由异常处理器决定；调试详情必须同时满足调试模式和本机回环请求门禁。
- 异常渲染失败会写日志，并继续返回已经转换的响应；HTTP 内核还有最外层恢复逻辑，用于处理请求解析、发送或 Recovery 自身边界之外的 panic。

Recovery 不会把 panic 重新抛给下游，也不会在响应已经写出后尝试拼接新的响应体。异常响应仍会经过 HTTP 内核的压缩关闭、terminate、访问日志和 `HttpEnd` 收尾流程。
