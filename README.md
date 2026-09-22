# ThinkGo Framework

`github.com/zhuhanxin0308/thinkgo/v3` 是 ThinkGo 的公共 Go 模块，框架源码位于仓库根目录。业务项目通过 CLI 创建，拥有独立的模块、配置和应用目录。

## 安装与创建项目

需要 Go 1.26.6 或更新版本，并将 Go 的可执行文件安装目录加入 PATH：

```bash
go install github.com/zhuhanxin0308/thinkgo/v3/cmd/thinkgo@v3.0.0
thinkgo create my-project
cd my-project
go test ./...
thinkgo list
thinkgo build linux/amd64
```

`create` 在当前目录下生成指定项目目录，包含 HTTP 和 CLI 入口、默认 index 应用、配置 JSON、语言包、模板、静态资源及测试。目标必须不存在或为空；隐藏文件和子目录也会使创建被拒绝。进入项目后可用 `thinkgo run` 启动服务，或用 `thinkgo make:app admin` 添加应用。

`go get github.com/zhuhanxin0308/thinkgo/v3@v3.0.0` 用于已有 Go 项目添加依赖；安装 CLI 使用上面的 `go install`。下游项目直接依赖公开版本，不需要克隆框架或添加本地 `replace`。

`build` 将二进制、配置 JSON、语言包、模板和静态资源写入 `dist/<平台>`，再次构建同一平台会覆盖该平台产物。Linux 产物还包含 `Dockerfile` 和 `docker-compose.yml`，可进入产物目录执行 `docker compose up -d --build`。发布包不包含 `.env` 和运行数据，数据库、端口等部署配置应在部署环境核对。

完整用法见[安装](docs/基础/安装.md)、[命令行](docs/命令行/README.md)和[目录结构](docs/基础/目录结构.md)。

## 原生多应用

框架通过编译期 `ApplicationDefinition` 清单运行一个或多个相互隔离的应用。每个应用拥有独立配置叠加、容器、Provider、路由、中间件、运行目录和关闭生命周期；HTTP 宿主按域名、路径映射或显式绑定选择应用。所有已编译应用会在网络监听前完成初始化和路由冻结，任一应用失败都会阻断启动。

```go
package main

import framework "github.com/zhuhanxin0308/thinkgo/v3"

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

v3 使用根标签 `v3.X.Y`，模块路径以 `/v3` 结尾。原有 v1、v2 标签保留；升级时需要同步修改依赖和 import 路径。发布工作流执行双 CGO 测试、Race Detector、逐包 80% 覆盖率、路由 fuzz、真实性能预算、MySQL/PostgreSQL/Redis/MongoDB/Neo4j 真实服务契约、静态与漏洞扫描，并生成 CycloneDX SBOM、SHA-256 校验和和构建 provenance。发布结果以 [GitHub Releases](https://github.com/zhuhanxin0308/thinkgo/releases) 和对应工作流状态为准，维护者流程见 [RELEASING.md](RELEASING.md)。

框架采用 Apache License 2.0，完整条款见本目录的 `LICENSE`。
