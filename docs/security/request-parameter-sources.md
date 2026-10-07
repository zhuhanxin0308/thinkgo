# 请求参数来源与授权对象

关联 issue #29。本改进不改变 ThinkPHP 兼容参数优先级，也不把来源分离当作授权实现。

## 选择确定的来源

| API | 来源与优先级 | 使用边界 |
| --- | --- | --- |
| `Request.Param` / `Request.All` | 路由、查询、请求体依次合并；请求体优先级最高 | 兼容的聚合输入，不用于隐式决定已授权对象 |
| `Request.Route` / `RouteValue` | 路由匹配及可信中间件通过 `SetRoute` 写入的数据 | 读取路径标识；仍需要对象级权限检查 |
| `Request.Get` | 查询参数 | 搜索、分页等明确来自 URL 查询的值 |
| `Request.Post` | 表单或 JSON 请求体 | 经校验且允许更新的业务字段 |

例如向 `/records/123?id=789` 提交 `{"id":"456"}`，`Route("id")` 是 `123`，`Get("id")` 是 `789`，`Post("id")` 和 `Param("id")` 是 `456`。显式空字符串也不能被误当作“没有提交”。不能通过改变调用顺序，让 `Param` 变成仅从路由读取。

## 授权和写入必须使用同一个 ID

错误的数据流是“按 `Route("id")` 检查权限，随后按 `Param("id")` 更新”。两次读取可能指向不同记录。

正确的数据流是：将 `Route("id")` 读入局部变量，验证其格式，使用已认证的可信身份检查该对象权限，再将同一个变量传给读取、更新或删除操作。只从请求体取得允许更新的字段，并执行字段校验；身份、owner、租户和授权结果不能直接相信请求中的同名值。输入来源一致不等于权限已经正确。

[可执行的 Route 授权示例](../../context/request_sources_security_example_test.go) 展示冲突参数的实际值，并用测试权限夹具说明一次读取、检查、操作的数据流。该夹具不是登录或多租户权限系统，业务必须实现自己的认证和对象权限。

[实际 HTTP 内核回归](../../http/issue_parameter_source_boundary_test.go) 覆盖 JSON、URL 编码表单、multipart、仅查询覆盖、显式空 ID，以及访问他人路由对象时伪造已授权 body ID 的场景。检查真实路由分发、兼容的 Param 值、响应状态和两条业务记录的最终状态。

## DTO 和中间件

使用 `binding.Input` 的来源标签（例如 `path:"id"`、`json:"name"`）可以显式分离字段。不能只开启未知字段拒绝，就宣称阻止了同名参数污染或越权。DTO 绑定后仍需使用绑定到路径来源的 ID 完成权限检查和持久化操作。

`SetRoute` 是可信代码的入口；中间件不应无条件把 `All()` 或请求体批量写回路由参数。否则来源边界被应用自身重新合并。写操作也不应因为使用 Route 而省略 CSRF、限流或身份认证。

## 验证

```sh
go test -race -count=1 -run '^ExampleRequest_Route_authorization$' ./context
go test -race -shuffle=on -count=10 -run '^TestIssue29AuthorizationUsesOneRouteID$' ./http
```

这些测试验证已有安全调用方式和文档示例，不宣称框架自动为任意业务增加对象授权，也不修改 `Param` 的兼容契约。
