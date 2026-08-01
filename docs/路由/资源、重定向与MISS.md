# 资源、重定向与 MISS

## 资源路由

```go
resource, err := router.Resource("/users", "User")
if err != nil {
	return err
}
if err := resource.Only("index", "read"); err != nil {
	return err
}
```

资源路由一次准备以下 7 个动作：

| 动作 | 方法 | 路径 | 处理器 |
|---|---|---|---|
| `index` | GET | `/users` | `User@Index` |
| `create` | GET | `/users/create` | `User@Create` |
| `save` | POST | `/users` | `User@Save` |
| `read` | GET | `/users/:id` | `User@Read` |
| `edit` | GET | `/users/:id/edit` | `User@Edit` |
| `update` | PUT | `/users/:id` | `User@Update` |
| `delete` | DELETE | `/users/:id` | `User@Delete` |

`Only` 保留指定动作，`Except` 排除指定动作。动作名会转为小写；未知动作返回 `ErrInvalidResourceAction`，`Only` 不提供动作也会失败。裁剪必须在冻结前完成，裁剪过程会同步更新路由名称索引。

资源路由可以在分组作用域内创建，因此会继承分组前缀、域名和中间件。

## 重定向

```go
_, err := router.Redirect("/old", "/new", http.StatusMovedPermanently)
```

目标不能为空，不能包含 CR、LF 或空字节。状态码默认 302；显式状态码只允许 301、302、303、307、308，且只能传一个。该 API 实际注册一个 `Any` 路由，处理器返回带目标地址和状态码的重定向响应。

## MISS 路由

```go
_, err := router.Miss(func(req *context.Request) *context.Response {
	return context.NewResponse().Code(404).Json(map[string]interface{}{
		"message": "not found",
	})
})
```

同一个 Router 只能注册一次 MISS 处理器。它只在显式路由和已启用的自动路由都未命中后返回；重复注册返回 `ErrDuplicateRoute`。

HTTP 内核没有 MISS 路由时，对 API 前缀未命中返回 404；非 API 路径会继续尝试 `public/index.html` 的 SPA fallback。需要自定义全局未命中响应时，应显式注册 MISS 路由并在处理器中返回明确状态。
