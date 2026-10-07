// Package context 封装框架的 HTTP 请求、响应及请求资源生命周期。
//
// # 参数来源与授权
//
// [Request.Param] 和 [Request.All] 是 ThinkPHP 兼容的混合来源 API：
// 请求体覆盖查询参数，查询参数覆盖路由参数。它们不适合隐式确定授权对象。
// 使用 [Request.Route] 读取路由 ID、[Request.Get] 读取查询值、[Request.Post]
// 读取表单或 JSON 请求体。对一个对象进行授权和更新时，应保存一次读取的 ID，
// 在权限检查和后续操作中使用同一变量；不要先用 Route 授权，再用 Param 更新。
//
// Route 也包含可信路由/中间件通过 SetRoute 写入的数据。来源分离不是授权：
// 调用者仍需使用可信身份执行对象级权限检查，并限制可更新字段。
// binding.Input 的 path/json/form/query 标签可用于显式分离 DTO 的字段来源；
// 未知字段检查本身不能代替来源选择或权限检查。
//
// 可执行示例见 [Request.Route]。仓库 docs/security/request-parameter-sources.md
// 说明混合来源契约、错误与正确的授权数据流，以及回归测试的覆盖边界。
package context
