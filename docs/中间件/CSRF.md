# CSRF

`framework/middleware` 提供签名双重提交 Cookie CSRF 中间件。它不依赖 Session：Cookie 中保存完整签名 token，状态变更请求必须同时提交同一个 token，框架使用 HMAC-SHA256 和常量时间比较校验。

## 创建

```go
config := middleware.DefaultCSRFConfig()
config.Secret = os.Getenv("CSRF_SECRET")
handler, err := middleware.CsrfWithConfig(config)
if err != nil {
    return err
}
pipeline, err := framework.ResolveServiceAs[*middleware.Pipeline](app, framework.ServiceMiddleware)
if err != nil {
    return err
}
pipeline.Alias("csrf", handler)
```

`Csrf()` 使用默认配置并生成进程级随机密钥。`CsrfWithConfig` 会复制 `SafeMethods`，创建后修改调用方切片不会改变中间件策略。应用初始化通过 `ParseCSRFConfig` 读取 `config/csrf.json`；若配置没有 Secret，框架优先复用 Cookie 工厂密钥，否则生成随机密钥。

应用初始化支持通过显式环境变量覆盖安全配置：`COOKIE_SECRET` 覆盖 `cookie.secret`，`CSRF_SECRET` 覆盖 `csrf.secret`，`APP_CSRF_ENABLE=true` 开启全局 CSRF。真实系统环境变量优先于 `.env`；密钥必须为 32 至 4096 字节，并且不会写入配置转储或日志。

## 默认配置

| 字段 | 默认值 |
|---|---|
| `CookieName` | `csrf_token` |
| `HeaderName` | `X-CSRF-Token` |
| `FieldName` | `_csrf` |
| `CookiePath` | `/` |
| `SameSite` | `Lax` |
| `MaxAge` | 7 天 |
| `SafeMethods` | `GET`、`HEAD`、`OPTIONS` |

Secret 非空时必须为 32 至 4096 字节；`MaxAge` 必须为 1 至 400 天。Cookie 名、Cookie 路径、HTTP 头名和表单字段名会做字符和长度校验，未知配置字段、类型错误和非规范 `SameSite` 会返回 `ErrInvalidCSRFConfig`。

## Token 格式与流程

token 格式为：

```text
v1.nonce.timestamp.signature
```

`nonce` 为 32 字节随机值的无填充 URL Base64，签名是绑定 Cookie 名、版本、nonce 和时间戳的 HMAC-SHA256。解析时会拒绝非规范 Base64、非法签名、重复 Cookie、超大 token、超过 5 分钟时钟偏差的未来 token 和超过 `MaxAge` 的过期 token。

安全方法的行为：

1. 读取并尝试验证 Cookie。
2. Cookie 缺失或无效时仍调用下游。
3. 下游返回非空响应且请求成功进入响应阶段时，生成新 token 并写入 `Set-Cookie`。
4. 已有合法 Cookie 或下游返回 `nil` 时不刷新。

安全方法出现重复同名 Cookie 会立即返回 400。随机源或 Cookie 写入失败返回 500。

非安全方法必须先有唯一的 CSRF Cookie，并通过签名和时间校验；随后从 `X-CSRF-Token` 或表单字段读取唯一提交值，并使用常量时间比较与 Cookie token 完全相等。支持 `application/x-www-form-urlencoded` 和 `multipart/form-data`，重复头、重复表单字段、畸形 Content-Type 和损坏 multipart 都返回 403，不调用下游。

URL 编码表单由框架 `Request` 解析缓存承载，CSRF 校验不会消费掉控制器后续读取 `Request.Body()` 所需的原始请求体；multipart 仍遵循请求体流式解析后的 `ErrRequestBodyUnavailable` 语义。

CSRF 错误响应使用 JSON `code`、`msg`、`data` 字段。开启全局 CSRF 的配置键是 `app.csrf_enable`；即使关闭全局启用，`csrf` 别名仍然可用于路由和控制器。
