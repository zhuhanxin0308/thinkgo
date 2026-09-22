# 资源、重定向与 MISS

## 资源路由

```go
Route.Resource("users", "user")
Route.Resource("articles", "article").Only("index", "read")
Route.Resource("orders", "order").Except("delete")
```

完整资源路由包含：

| 动作 | 方法 | 路径 | 处理器 |
|---|---|---|---|
| `index` | GET | `/users` | `user/index` |
| `create` | GET | `/users/create` | `user/create` |
| `save` | POST | `/users` | `user/save` |
| `read` | GET | `/users/:id` | `user/read` |
| `edit` | GET | `/users/:id/edit` | `user/edit` |
| `update` | PUT | `/users/:id` | `user/update` |
| `delete` | DELETE | `/users/:id` | `user/delete` |

`Only` 只保留指定动作，`Except` 排除指定动作。资源路由可以在 `RuleGroup` 中创建，并继承分组前缀、域名和中间件。

## 重定向

```go
Route.Redirect("old", "/new")
Route.Redirect("legacy", "/new", http.StatusPermanentRedirect)
```

默认状态码与 ThinkPHP 一致为 301；显式状态码只允许 301、302、303、307 和 308。目标不能包含回车、换行或空字节。

## MISS 路由

```go
Route.Miss(func(request *framework.Request) *framework.Response {
	return framework.NewResponse().Code(404).Json(map[string]interface{}{
		"message": "not found",
	})
})

Route.Miss("error/notFound", "POST")
```

MISS 在当前请求方法的显式路由未命中后执行，并优先于默认 URL 调度。可以分别声明方法专用 MISS 和通用 MISS；当前方法没有专用规则时回落到通用规则。

没有 MISS 时，API 路径未命中返回 404；非 API 路径会继续尝试 `public/index.html` 的 SPA fallback。
