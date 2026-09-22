# URL 访问

请求 URL 先由项目级解析器选择编译期应用，再进入该应用路由。选择优先级为显式 `Http.Name()`、域名绑定、路径映射/应用名、默认应用；被禁止或未知的应用返回 404，危险路径返回 400。

```text
http.Request
    ↓
Host 与编码路径校验
    ↓
domain_bind
    ↓
app_map / app 名称 / default_app
    ↓
ApplicationContext
    ↓
目标应用 Route
```

例如 `app_map.backend=admin` 时，公开请求 `/backend/users` 在 admin 应用内匹配 `/users`，上下文保留公开前缀 `/backend`。域名绑定 `admin.example.com=admin` 时，`/users` 直接进入 admin。

## 请求 URL

`Request.Url()` 默认返回包含查询的相对请求目标，传入 `true` 返回绝对地址；`BaseUrl()` 去除查询，`Root()` 返回当前应用公开根，`Domain()` 返回协议和域名。

绝对 URL 只使用通过 allowed hosts 与可信代理规则验证的值。请求路径拒绝危险编码、反斜杠、空字节和 `..` 穿越。

## 路由 URL

命名路由从当前应用 Router 生成：

```go
path, err := app.Route().URL("user.show", map[string]interface{}{"id": 10})
absolute, err := app.RouteURL(request, "user.show", map[string]interface{}{"id": 10})
```

请求带有 `ApplicationContext` 时，完整 URL 会恢复当前域名绑定或路径映射前缀，确保生成地址仍能路由回同一应用。没有请求上下文时使用应用与项目配置的规范域名和前缀。

路由参数按名称绑定，剩余标量进入查询。复杂对象、非有限浮点数、缺少必需参数或不满足约束都会返回错误。

## 安全约束

- Host 必须是合法域名、IP 或带合法端口的 Host；IPv6 使用方括号。
- 域名通配规则在启动期编译，未知目标应用会阻断启动。
- 重定向、下载文件名和响应头不得直接拼接未校验输入。
- `public` 只放允许公开访问的文件；需要鉴权的资源通过应用控制器返回。

完整顺序见[请求流程](请求流程.md)，路由定义见[路由](../路由/README.md)。
