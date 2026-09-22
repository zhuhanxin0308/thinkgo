# Trace

`framework/middleware.Trace` 在开发调试时把请求信息、SQL、缓存、日志、加载文件和调试变量渲染成 HTML 调试面板。它的输出门禁比“调试开关已打开”更严格，避免把内部信息暴露给远程客户端。

Trace 使用 `debug.NewRequestDebug(true)` 为每个请求创建私有 collector，并在请求结束时清理；不会把当前请求写入全局 Debug 单例。collector 对日志、SQL、缓存、文件和变量分别设置固定上限（分别为 200、100、200、200、100），超过上限只标记对应类别的 `truncated` 状态，不继续分配内存。视图和缓存通过 `RenderWithDebug`、`FetchWithDebug`、`WithDebug` 接收显式 collector，旧的构造级入口继续保留兼容行为。

## 启用条件

框架只有在 `app.app_trace=true` 时将 Trace 加入全局管道。每个请求还必须同时满足：

- 底层 `RemoteAddr` 是 IPv4 或 IPv6 回环地址。
- `X-Forwarded-For`、`X-Real-IP` 和 `Forwarded` 中出现的每个地址都能解析为回环地址。
- 代理头中的地址不能是未知值、非法值或远程地址。

开启 Trace 不会放宽这些请求来源条件。请求未通过门禁时仍会继续业务处理，但 `_debug` 数据会被清理，不会注入面板。

## 响应注入

Trace 把请求级调试对象写入 `_debug`，记录处理开始时间和耗时。只有响应 `Content-Type` 为空或包含 `text/html` 时才考虑注入；JSON、二进制和其他明确类型不注入。HTML 检测不区分大小写：存在 `</body>` 时插入其前面，否则存在 `</html>` 时追加到响应末尾；两者都不存在则保持原体。

面板中的请求 URI、IP、SQL、缓存键、日志消息、文件路径和变量键值都先经过 HTML 转义。调试资源不内联，而是使用：

```text
/__thinkgo_debug__/trace.css
/__thinkgo_debug__/trace.js
```

这两个路径只有通过本机来源门禁时才由 Trace 直接返回，响应使用 `private, max-age=300` 缓存策略。资源请求不会进入业务下游。

## 使用边界

Trace 位于全局管道，因此静态 `public` 文件请求也会经过 Trace；但静态文件由原生 `ResponseWriter` 直接提交，框架不会把文件实体重新装入 `context.Response`，所以不会向静态 HTML 注入调试面板。Trace 结束时清理请求调试对象；应用不应把调试对象保存到全局变量或在生产环境依赖 Trace 面板展示业务数据。
