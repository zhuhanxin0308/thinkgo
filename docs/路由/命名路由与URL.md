# 命名路由与 URL

使用链式 `Name` 设置路由标识：

```go
Route.Get("users/:id", "user/read").Name("users.read")
```

名称必须以字母或下划线开头，后续只能使用字母、数字、点、短横线和下划线，最长 128 字节，并且不能重复。

## 生成路径

```go
path, err := app.Route().URL("users.read", map[string]interface{}{
	"id":  100,
	"tab": "profile",
})
```

结果为 `/users/100?tab=profile`。路径参数按声明顺序消费，未消费参数按键名排序后进入查询字符串。

必填参数缺失、值为 `nil`、类型不支持、正则约束不满足或命名路由不存在时返回错误。可选参数可以缺省；参数会执行 URL 转义，`Ext` 约束会追加到最后一个路径段。

## 生成完整 URL

```go
absolute, err := app.RouteURL(request, "users.read", map[string]interface{}{
	"id": 100,
})
```

应用还提供：

```go
app.Domain()
app.URL("/login")
app.AssetURL("app.css")
```

`app.Domain()` 默认使用 `https://localhost`，可由 `SERVER_DOMAIN` 覆盖。带请求上下文生成完整 URL 时会恢复当前应用的域名绑定或 `app_map` 公开前缀；只有一个 index 应用时保持无前缀兼容结果。
