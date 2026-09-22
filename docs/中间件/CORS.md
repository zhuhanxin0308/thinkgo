# CORS

框架从 `config/cors.json` 严格构造 CORS 策略，并始终注册 `cors` 中间件别名。`enable=true` 时，该策略按默认装配顺序位于 Recovery 之后，安全响应头、限流、Session、CSRF 和应用业务中间件之前；默认关闭，不会擅自开放跨域访问。未知字段、错误类型、非法来源、空方法列表或不安全通配符会阻止应用启动。

## 配置

```json
{
  "enable": true,
  "allow_origins": ["https://web.example.com"],
  "allow_methods": ["GET", "POST"],
  "allow_headers": ["Content-Type", "Authorization"],
  "expose_headers": ["X-Request-ID"],
  "allow_credentials": true,
  "max_age": 600
}
```

部署实例可用 `[CORS]` 段或同名系统环境变量覆盖，例如 `CORS_ENABLE`、`CORS_ALLOW_ORIGINS` 和 `CORS_ALLOW_CREDENTIALS`。数组值使用 JSON 数组格式。配置文件保留结构和安全默认值，真实前端来源由部署环境维护。

代码直接构造策略时使用返回错误的入口：

```go
handler, err := middleware.NewCors(middleware.CorsConfig{
    AllowOrigins:     []string{"https://web.example.com"},
    AllowMethods:     []string{"GET", "POST"},
    AllowHeaders:     []string{"Content-Type", "Authorization"},
    ExposeHeaders:    []string{"X-Request-ID"},
    AllowCredentials: true,
    MaxAge:           600,
})
if err != nil {
    return err
}
```

`CorsWithConfig` 仅用于兼容旧代码，配置错误时会返回拒绝请求的处理器；新代码应使用 `NewCors` 在启动阶段暴露错误。`Cors()` 使用公开、无凭证的默认策略：来源为 `*`，方法为 `GET`、`POST`、`PUT`、`DELETE`、`PATCH`、`OPTIONS`，预检缓存 86400 秒。

## 来源、凭证与缓存

- `allow_origins` 恰好只有 `*` 时使用通配来源，并且不能启用凭证。
- 显式来源按浏览器序列化后的 `Origin` 精确匹配；命中后才会回显来源并允许凭证。
- 凭证模式下，方法、请求头和暴露头也不能使用 `*`。
- `Authorization` 不受允许请求头的 `*` 覆盖，必须显式列入 `allow_headers`。
- 显式来源策略的响应始终合并 `Vary: Origin`，未命中或没有 Origin 时也不例外，避免共享缓存混用结果。

来源未命中的普通请求仍会进入下游，但不会获得允许来源响应头。CORS 只限制浏览器读取响应，不是认证、授权或 CSRF 防护；需要拒绝请求本身时必须使用独立保护中间件。

## 预检与普通请求

只有同时具备 `OPTIONS`、`Origin` 和非空 `Access-Control-Request-Method` 的请求才是预检。普通 OPTIONS 会继续进入路由，保留框架自动生成的 `Allow` 响应。

预检会严格校验目标方法和 `Access-Control-Request-Headers`。合法预检直接返回 204，只携带允许来源、凭证、方法、请求头和缓存时间；普通响应只携带允许来源、凭证及暴露头。预检响应同时合并 `Vary: Origin`、`Access-Control-Request-Method` 和 `Access-Control-Request-Headers`。

全局 CORS 会覆盖静态文件、框架响应和标准库 `http.Handler`。中间件会在调用下游前写入原生 `ResponseWriter`，并在下游返回后再次写入框架响应，避免直接提交响应时丢失头部。

## 路由与分组策略

需要不同路由使用不同来源策略时，可把 `NewCors` 返回的处理器放在路由或分组中，并确保它排在鉴权等可能短路的中间件之前。ThinkPHP 默认自动 OPTIONS 不读取 `Access-Control-Request-Method`，也不继承其它方法路由的中间件；因此路由级 CORS 必须同时显式注册对应 OPTIONS 路由。应用级 CORS 位于路由前，可以直接完成所有合法预检。
