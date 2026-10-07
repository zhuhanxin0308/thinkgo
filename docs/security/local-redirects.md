# 站内重定向

处理 issue #32：新增 `Response.RedirectLocal(target, code...)`。已有 `Redirect` 继续支持应用明确选择的站外地址，不将旧 API 的合法用途视为缺陷。

```go
response := context.NewResponse().RedirectLocal("/account?tab=profile", http.StatusSeeOther)
// 返回 response，由框架发送；自行发送时也必须处理 response.Send(writer) 的错误。
```

仅接受以单个 `/` 开头的根相对 URL。默认 302，可显式使用 301、302、303、307、308。拒绝 scheme、authority、userinfo、反斜线、原始控制字符、首尾空白、非法 UTF-8、非法路径转义、解码后双斜线开头/控制字符/反斜线，以及 `.`、`..` 路径段。查询字符串和片段按原文保留，不被当作新的目标 URL。

无效目标、无效状态或多个状态参数会记录可由 `errors.Is(err, context.ErrInvalidRedirect)` 识别的错误，清除已有 Location，并在发送时沿用通用 500 响应；不会执行之前配置的流回调或发送其私有实体。错误消息不回显用户提交的目标。零值和 nil receiver 保持 Response 的既有约定。

这不是目标路由授权，也不能防止站内路由再次发起外跳。下游不能先检查后另行解码、拼接或替换 Location。可执行示例、危险输入表和 fuzz 回归与实现一起提交。
