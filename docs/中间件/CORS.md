# CORS

`framework/middleware.CorsWithConfig` 提供通用 CORS 中间件。它只在调用方把返回的 `Handler` 放入全局、路由或分组管道后生效，应用初始化不会自动安装通用 CORS。

## 配置

```go
handler := middleware.CorsWithConfig(middleware.CorsConfig{
    AllowOrigins:     []string{"https://web.example.com"},
    AllowMethods:     []string{"GET", "POST", "OPTIONS"},
    AllowHeaders:     []string{"Content-Type", "Authorization"},
    ExposeHeaders:    []string{"X-Request-ID"},
    AllowCredentials: true,
    MaxAge:           600,
})
```

`Cors()` 使用默认配置：来源为 `*`，方法为 `GET`、`POST`、`PUT`、`DELETE`、`PATCH`、`OPTIONS`，请求头包含 `Content-Type`、`Authorization`、`X-Requested-With`、`Accept`、`Origin` 和 `token`，不允许凭证，预检缓存 86400 秒。

## 来源与凭证

- `AllowOrigins` 恰好只有 `*` 时使用通配来源，并且永不设置 `Access-Control-Allow-Credentials`。
- 显式来源模式按请求 `Origin` 精确匹配；只有命中白名单才允许凭证。
- 通配来源和凭证不会组合，也不会把任意请求来源反射到响应。
- 显式来源响应会设置 `Vary: Origin`，避免共享缓存混用不同来源的响应。

非通配模式下没有 Origin 的普通请求继续下游且不添加 CORS 头；没有 Origin 的 OPTIONS 返回 403。显式来源未命中时，普通请求继续下游但不添加 CORS 头，OPTIONS 直接返回 403。

## 预检与普通请求

允许来源的 OPTIONS 不调用下游，直接返回 204，并添加允许来源、方法、请求头、可选暴露头、缓存时间和凭证头。通用 CORS 不解析 `Access-Control-Request-Method` 和请求头的细粒度合法性；需要严格预检白名单时使用管理员 CORS 或自定义中间件。

允许来源的普通请求调用下游，并向返回响应添加 CORS 头。下游返回 `nil` 时通用 CORS 用 204 响应兜底，以便写入响应头。
普通跨域请求同样必须命中 `AllowMethods`；未列入的方法直接返回 403，不会调用下游。没有 `Origin` 的普通请求保持原样，不添加 CORS 响应头。
