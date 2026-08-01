# URL访问

ThinkGo 的 URL 访问包含两件事：统一宿主如何从请求中选择应用，以及应用如何生成当前或目标应用的 URL。应用选择发生在路由匹配之前，URL 生成必须尊重当前请求的应用作用域。

## 请求 URL 进入应用

`framework/http.MultiHttp` 先调用 `ApplicationResolver.Resolve`：

```text
Host / URL 路径校验
        ↓
domain_bind
        ↓
路径首段：应用名或 app_map
        ↓
deny_app_list / app_express
        ↓
ApplicationResolution
        ↓
克隆请求并重写路径
        ↓
目标应用 Http -> 路由匹配
```

例如：

| 原始请求 | 选择方式 | 目标应用看到的路径 |
|---|---|---|
| `/users` | `default_app=index` | `/users` |
| `/admin/users` | `admin` 或 `app_map.backend=admin` | `/users` |
| `admin.example.com/users` | `domain_bind` | `/users` |

解析器保留 `OriginalPath`，只在克隆请求上使用 `RewrittenPath`。它不会改变调用方持有的原始 `http.Request`，也不会把未校验的路径交给应用路由。

## 应用路由

路由定义位于应用自己的 `route` 包：

```go
// app/index/route/app.go
package route

import "thinkgo/framework"

func Load(app *framework.App) error {
	_, err := router.Get("/users/:id", "User@Show")
	return err
}
```

应用入口在 `app/index/application.go` 中注册路由加载器：

```go
if err := app.RegisterRouteLoader(route.Load); err != nil {
	return err
}
```

控制器字符串只在当前应用的控制器注册表中解析。路由冻结后不能继续添加路由；动态修改路由必须在下一次应用启动时完成。

## 命名路由 URL

```go
path, err := router.URL("user.show", map[string]interface{}{
	"id": 100,
	"tab": "profile",
})
if err != nil {
	return err
}
```

在请求处理期间，优先使用带请求上下文的方法，让应用前缀、域名绑定和 Host 作用域自动生效：

```go
url, err := app.RouteURL(req, "user.show", map[string]interface{}{
	"id": 100,
})
```

路径参数按路由声明顺序消费，剩余参数按键名排序进入查询字符串。必填参数缺失、路由不存在、约束不满足或参数类型不支持时返回错误；不会为任意值调用不受控的 `String()`。

## 当前应用 URL

```go
loginURL := app.URLFor(req, "/login")
assetURL := app.AssetURLFor(req, "assets/app.css")
```

- 当前请求由路径选择应用时，`/login` 会自动恢复应用路径前缀。
- 当前请求由域名绑定应用时，URL 使用请求 Host 和应用路径。
- `http` 或 `https` 的完整 URL 会按完整 URL 处理；其它绝对形式、控制字符和非法路径返回空字符串或错误。
- 没有请求上下文时，`app.URL`、`app.AssetURL` 仍可生成当前应用的基础域名 URL，但不会推断其它应用的路径别名。

## 跨应用 URL

应用管理器可以根据目标应用的域名绑定、应用映射别名或应用名生成 URL：

```go
dashboard, err := app.URLForApplication(req, "admin", "/dashboard")
adminRoute, err := app.RouteURLForApplication(
	req,
	"admin",
	"dashboard",
	nil,
)
```

选择规则：

1. 目标应用有精确域名绑定时使用该 Host。
2. 没有域名绑定且有 `app_map` 别名时使用最稳定的别名。
3. 否则使用应用名作为路径前缀。
4. 目标应用在 `deny_app_list` 中或不存在时返回错误。

跨应用 URL 只生成目标应用入口，不共享目标应用的控制器、Session 或请求状态。若业务需要跨应用调用，应通过服务接口、消息或明确的 HTTP 请求完成。

## 路径和输出安全

- 不要把用户输入直接作为应用名、应用映射别名或域名绑定键。
- 不要把未校验的路径拼接到 `Location`、文件路径或静态资源 URL。
- `%2f`、`%5c`、`%2e%2e`、`%00` 等危险编码在应用选择和应用 URL 构造中都会被拒绝。
- 需要公开的静态资源放在项目 `public/`；私有文件经过控制器和服务授权后通过安全文件响应接口返回。

详细路由规则见[命名路由与 URL](../路由/命名路由与URL.md)、[路由匹配](../路由/路由匹配.md)和[路由错误与安全](../路由/路由错误与安全.md)。
