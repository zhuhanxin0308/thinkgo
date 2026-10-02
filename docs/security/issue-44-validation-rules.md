# 控制 OpenAPI 校验规则原文的导出

`RegisterTyped` 和 `Handle` 自动生成的 Schema 默认保留
`x-thinkgo-validation`。发布文档时可显式关闭这一扩展：

```go
registry, err := openapi.NewRegistry(
    openapi3.Info{Title: "Public API", Version: "1"},
    openapi.WithValidationRules(false),
)
if err != nil {
    return err
}
```

省略选项或设置为 `true` 保留原有行为。选项传给 `NewRegistry`，应在
注册与发布前确定；冻结后的注册表拒绝修改选项。

过滤仅发生在首次导出的独立 JSON 快照上。命名组件、内联请求/响应
Schema、参数、响应头、回调和 `json,string` 的 `contentSchema` 中的
该规则扩展均可移除；`JSON` 和 `Handler` 使用同一份过滤后的快照。
原有完整文档校验、外部引用禁用、ETag、HEAD 和条件请求保持不变。

标准 Schema 约束（例如 `required`、数值范围和字符串长度）仍然保留。
运行时绑定和验证计划不受影响。关闭文档注解不会放宽输入规则。

该选项不是全局字符串脱敏器：名为 `x-thinkgo-validation` 的业务字段、
组件名称、`$ref`、示例、默认值、枚举值及自定义扩展内容不会被删除。
不要把密钥写入文档、注释、规则或示例；标准约束和描述仍可能体现业务规则。

回归命令：

```sh
go test -race -mod=readonly -shuffle=on -count=20 -run '^TestIssue44' ./openapi
go test -race -mod=readonly -shuffle=on -count=3 ./openapi ./binding ./route
```

此项仅在修复合并 `main` 且对应主分支提交的完整 CI、回归验证通过后关闭。
