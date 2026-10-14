# 显式严格安全检查

`app.security_strict` 允许预发、测试或开发应用显式执行生产级配置检查，而不把 `app_env` 改成 `production`。这是初始化准入；浏览器安全默认值由 `browser_cookie` 档案选择，严格模式另外禁止 Cookie 属性和 Session ID 策略降级。

## 启用方式

在应用初始化前，在 `config/app.json` 中配置。以下示例用于不使用浏览器 Cookie 凭据的无状态 API：

```json
{
  "app_env": "staging",
  "app_debug": false,
  "security_strict": true,
  "security_profile": "stateless_api",
  "session_enable": false,
  "csrf_enable": false,
  "server": {
    "allowed_hosts": ["staging.example.test"]
  }
}
```

`allowed_hosts` 应由部署者指定客户端实际访问的主机，不能从首次请求、监听地址或不可信代理头推导。这里的域名仅为示例，必须替换为应用自己的域名。监听地址及其他服务器选项继续使用原有配置，不被此开关改写。

| 环境与开关 | 行为 |
| --- | --- |
| `production`、`prod`、`release`，包括既有大小写及首尾空白规范化 | 总是执行原有生产检查，`false` 不能禁用。 |
| 其他环境，缺省或 `false` | 保持既有行为；不把非生产环境的空白名单偷偷替换为某个主机。 |
| 其他环境，`true` | 执行同一套生产级检查，失败进入 `StartupError` 并阻止应用运行。 |
| 显式空值或非法类型/字符串 | 进入启动错误链，不能静默解释为关闭检查。 |

开关沿用应用现有的严格布尔解析：JSON 中使用 `true`/`false`；已有配置解析器接受的布尔字符串 `true`、`false`、`1`、`0` 也保持有效，数字类型不作为布尔值。设置开关不会改变 `Environment()`、调试开关或认证档案。

## 检查的实际范围

两种认证档案均须关闭调试、显式声明 `security_profile`、配置非空且不包含全匹配 `*` 的 Host 白名单；Host 语法及实际请求匹配仍由原有 HTTP 配置路径负责。

`stateless_api` 须关闭 Session 和 CSRF Cookie，不会强制创建浏览器 Cookie 凭据。此档案不证明业务确实没有读取自定义认证 Cookie，业务不得用它绕过浏览器认证所需的保护。

`browser_cookie` 须启用 Session，设置 Cookie/Session 密钥及 `cookie.secure=true`，启用 CSRF 并设置有效密钥和 `csrf.secure=true`；CSRF 仍可按现有规则复用 Cookie 密钥。开关不生成生产密钥。

显式 `security_strict=true` 的浏览器应用还须设置 `cookie.httponly=true`、`cookie.samesite="Lax"` 或 `"Strict"`、`session.reject_unknown_id=true`；实际 Session Cookie 的 `session.samesite` 也必须为 Lax 或 Strict。仅配置通用 Cookie 工厂为 Lax 不能允许 Session 覆盖成 None。生产环境缺省 strict 时保留原有检查范围，启用 strict 才增加这些准入要求；无状态 API 不要求这些 Cookie 属性。

失败的非生产应用使用“严格模式安全警告/检查失败”诊断；已有生产环境的诊断保持不变。同一应用按既有分类去重，重复检查不重复记日志。日志过滤或输出失败不取消已经记录的启动错误。实际 HTTP 初始化失败返回通用 500，不把内部配置错误回显给请求方；有效配置下不在白名单中的 Host 继续返回 421，业务处理器不执行。

## 与其他安全能力的边界

通用 Cookie 包的默认值保持兼容，严格浏览器应用使用上述准入检查。独立创建的工厂和业务显式单次选项仍须按 [敏感 Cookie 策略](cookie-policies.md) 审查。JavaScript 可读偏好 Cookie 应使用独立且不承载身份的工厂。跨站 SSO 或嵌入协议需要单独评估，不应使用严格档案后期待 None 被忽略。`SameSite` 不能普遍替代 CSRF 防护，参考 [OWASP CSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)。

`browser_cookie` 下缺省 `app.csrf_enable` 和 `security_headers.enable` 现在默认开启；`stateless_api` 和未使用 Session/CSRF 的旧配置保持关闭。没有显式档案、但启用 Session 的应用会推导为 `browser_cookie`，同样采用浏览器默认值。已有显式 false 不被覆盖；生产和严格模式拒绝关闭 CSRF。安全响应头可通过显式 false 退出，CSP/frame/COOP 等也可按原配置定制。

升级前应检查已有页面的内联脚本、CDN、跨域资源和嵌入策略，并配置合适的安全头；表单/JavaScript 的状态变更请求必须提交有效 CSRF token。显式关闭只用于已有经过评估的兼容流程，不意味着该应用已受这些保护。默认值只更新当前实例和中间件，不写回冻结项目配置，无状态兄弟应用不会继承浏览器实例的运行时开关。

严格模式不配置 TLS 证书、可信代理、限流、上传策略、数据库连接预算或网关规则，也不替业务执行对象授权或凭据验证。认证和权限提升须使用请求 Session 的 `SetAuthenticated` 或正确调用 `Regenerate`，并处理最终持久化失败，详见[会话认证边界](session-identity.md)。现有 `app_env=production` 的其他专用诊断不会因这个开关自动推广到预发。

应把它配置到每个需要保护的 App 的最终配置中，并在初始化前完成配置合并；多应用配置继承与覆盖规则不变，不宣称测试了所有多应用组合。它不是运行时热更新或持续检查机制；初始化失败后不能通过修改配置或关闭开关让失败对象恢复运行，应修正配置并新建应用。

应用初始化可能已经创建日志、缓存等资源；失败并不代表整个初始化没有副作用。应使用 `BuildApp` 处理构建错误，或在使用未初始化构造入口时始终关闭应用资源。此检查也不能替代请求、进程、连接及临时磁盘总预算。
