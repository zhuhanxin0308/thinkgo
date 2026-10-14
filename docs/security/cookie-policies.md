# 通用 Cookie 与认证 Cookie 的显式策略

通用 `cookie.DefaultConfig()` 和 `cookie.NewCookie(nil)` 不是认证安全 profile：包级默认 `HttpOnly=false`，`SameSite` 为空，`Secure=false`。HTTPS 请求或可信协议结论可以使实际头部带上 Secure，但不应以此替代敏感 Cookie 的显式配置。脚手架中的配置、Session Cookie 的强制 HttpOnly，以及生产启动门禁，不能替代直接调用通用包时的策略。

直接包调用保持上述默认值。应用可选择 `app.security_profile="browser_cookie"` 与 `app.security_strict=true`，初始化会要求通用 Cookie 的 Secure、HttpOnly、Lax/Strict SameSite，以及 Session 严格 ID 采纳和实际 Session SameSite，详见[严格安全检查](strict-security.md)。这是应用级强制策略；独立工厂与单次属性覆盖仍需审查。

## 对敏感 Cookie 显式配置

```json
{
  "secure": true,
  "httponly": true,
  "samesite": "Lax",
  "path": "/",
  "expire": 900
}
```

这是通用 Cookie 工厂的策略示例，不是完整应用配置或适合所有业务的有效期。未指定 Domain 时保留仅当前主机的范围；应用部署在更窄路径时按业务调整 Path。跨站登录或嵌入流程需要单独评估 SameSite，不能为消除登录故障而随意放宽所有认证 Cookie。

对应可执行 Go 示例见 `cookie/sensitive_policy_test.go` 中的 `ExampleNewCookieWithConfig_sensitivePolicy`：从默认配置取得值副本，显式设置安全属性，再创建工厂。处理真实请求时调用 `factory.ForRequest(req, writer)`，检查错误后调用请求级实例的 `Set`，并在提交响应前处理其错误。不要把请求级实例共享给其他请求。示例只输出属性检查结果，不输出凭据或完整 Set-Cookie。

这里的 token 字符串仅用于演示头部构造，不是可上线的认证实现。真实 Cookie 应保存服务端生成、可验证并具有撤销和过期策略的不透明凭据，不要将裸用户 ID 或角色名称当成身份凭证。存在框架 Session 流程时，优先使用请求级 Session，而不是自行复制认证生命周期。

`Secure` 不会自动将登录请求升级为 HTTPS，也不会加密已经发送的明文响应。登录和后续认证流量仍须使用 HTTPS；不要在明文响应中发出凭据后期待浏览器的 Secure 规则补救。HttpOnly 防止脚本直接读取 Cookie，不阻止 XSS 代用户发送请求；SameSite 是额外防御，不替代 CSRF 校验、方法约束和业务授权。认证成功或权限变化后的会话轮换参见[会话 ID 与认证轮换](session-identity.md)。

## 签名、业务值与公开偏好设置

配置非空 `secret` 会启用该包已有的签名验证；密钥必须由可信密钥管理提供，不要把示例字符串用作生产密钥。必须处理 `Get` 的值、存在标志和错误，验签失败不能继续采用输入内容。签名保证与加密不同，不能把敏感明文放入 Cookie 后认为签名会隐藏它。

公开的语言或主题偏好可能需要 JavaScript 读取。可为这种不承载身份的用途创建独立工厂，并显式选择 `httponly=false`；不要因此降低认证 Cookie 的策略。不同工厂和 `GetConfig()` 返回副本不共享可变配置。

现有 `CookieOptions` 对 Secure 和 HttpOnly 使用保留已开启值的合并规则；仅设置 `Expire` 不会清除工厂中的这两个标志。SameSite、Path、Domain 等显式覆盖仍可能扩大范围，须审查调用处。错误的属性会被拒绝，而不是输出部分成功的 Set-Cookie。固定属性不能防止应用自己选用另一份不安全配置。

## 回归与验证边界

新增协议回归检查实际头部：显式策略在 HTTP/HTTPS 请求模型下均保留；只覆盖有效期时属性和主机范围不丢失；修改原配置、配置副本或其他工厂不污染敏感策略；非法选项返回 `ErrInvalidCookieOptions` 且不写 Cookie。HTTP 请求模型用于检验头部构造，不代表推荐在明文网络传送令牌。

这些是 Go HTTP 头部构造与隔离测试，不是浏览器 XSS/CSRF、反向代理或 SSO 的端到端验证，也不验证 token 的生成、账户认证或服务端撤销。默认属性保持兼容；严格档案的初始化回归另外验证属性缺失或降级阻止运行。

依据：[OWASP Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html) 的 Secure、HttpOnly、SameSite 及 Domain/Path 章节。框架的具体合并和错误行为以 `cookie/cookie.go` 及相应回归为准。
