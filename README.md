# ThinkGo Framework

`github.com/zhuhanxin0308/thinkgo/framework` 是 ThinkGo 唯一对外发布的 Go 模块。仓库根模块仅用于框架开发、兼容性验证和示例宿主，不发布应用二进制或应用版本。

## 发布状态与安装

当前仓库没有与源码版本对应的 `framework/v1.0.0` 标签和 GitHub Release 证据，因此不能把 `v1.0.0` 描述成已经可获取的公共版本。发布工作流成功后，请从实际 Release 复制版本号替换下方的 `vX.Y.Z`：

```bash
go get github.com/zhuhanxin0308/thinkgo/framework@vX.Y.Z
```

下游项目必须直接依赖公开版本，不能依赖本仓库的 `go.work` 或本地 `replace`。

## 原生多应用

框架通过编译期 `ApplicationDefinition` 清单运行一个或多个相互隔离的应用。每个应用拥有独立配置叠加、容器、Provider、路由、中间件、运行目录和关闭生命周期；HTTP 宿主按域名、路径映射或显式绑定选择应用。所有已编译应用会在网络监听前完成初始化和路由冻结，任一应用失败都会阻断启动。

```go
package main

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func registerGlobalComponents(*framework.App) error { return nil }
func registerIndexComponents(*framework.App) error  { return nil }
func registerAdminComponents(*framework.App) error  { return nil }

func main() {
    application := framework.NewAppUninitialized(".")
    defer func() {
        if err := application.Close(); err != nil {
            panic(err)
        }
    }()

    err := application.RegisterApplications(
        registerGlobalComponents,
        framework.ApplicationDefinition{Name: "index", Register: registerIndexComponents},
        framework.ApplicationDefinition{Name: "admin", Register: registerAdminComponents},
    )
    if err != nil {
        panic(err)
    }
}
```

业务项目通常由 `service:discover` 生成上述清单，不需要手工维护注册表。项目级 `app.default_app`、`app.app_map`、`app.domain_bind`、`app.deny_app_list` 和 `app.app_express` 决定请求解析；应用目录下的配置不能反向修改宿主监听地址或项目级解析规则。

## 发布与安全

框架版本使用 `framework/vX.Y.Z` 仓库标签，对应模块版本 `vX.Y.Z`。发布工作流执行双 CGO 测试、Race Detector、逐包 80% 覆盖率、路由 fuzz、真实性能预算、MySQL/PostgreSQL/Redis/MongoDB/Neo4j 真实服务契约、静态与漏洞扫描，并附带 CycloneDX SBOM、SHA-256 校验和和构建 provenance。

框架采用 Apache License 2.0，完整条款见本目录的 `LICENSE`。
