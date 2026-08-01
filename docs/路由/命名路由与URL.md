# 命名路由与 URL

命名路由通过 `WithName` 设置。名称必须以字母或下划线开头，后续只能使用字母、数字、点、短横线和下划线，最长 128 字节；名称在同一 Router 内唯一。

```go
detail, err := router.Get("/users/:id", "User@Show")
if err != nil {
	return err
}
if err := detail.WithName("users.show"); err != nil {
	return err
}
```

## 生成路径

```go
url, err := router.URL("users.show", map[string]interface{}{
	"id": 100,
	"tab": "profile",
})
```

结果为 `/users/100?tab=profile`。路径参数按路由声明顺序消费，未消费的参数按键名排序后进入查询字符串；`[]string` 查询值会重复生成同名参数。

必填参数缺失、值为 nil、类型不支持、正则约束不满足或命名路由不存在时返回 `ErrInvalidRouteParameter`。

路径参数支持字符串、布尔值、各种整数、有限浮点和 `json.Number`。NaN、无穷、复杂对象以及需要调用自定义 `String()` 的类型会被拒绝；框架不会为生成 URL 调用任意对象的方法。

可选参数可以缺省；提供空字符串时会省略该段。参数和静态段都会进行 URL 转义，扩展名约束会追加到最后一个路径段。

## 应用级 URL

应用还提供：

```go
app.Domain()
app.URL("/login")
app.AssetURL("app.css")
```

`app.Domain()` 默认使用 `https://localhost`，可由 `SERVER_DOMAIN` 覆盖。完整 URL 仅接受带 host 的 HTTP(S) 地址；域名/路径中包含 CR、LF、TAB 或缺少 host 时返回空字符串。拼接时会消除域名和路径之间的重复斜线。
