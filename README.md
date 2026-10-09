# ThinkGo Framework

`github.com/zhuhanxin0308/thinkgo/v3` 是 ThinkGo 的公共 Go 模块，框架源码位于仓库根目录。业务项目通过 CLI 创建，拥有独立的模块、配置和应用目录。

[安装与创建项目](#安装与创建项目) · [原生多应用](#原生多应用) · [路由安全默认值](#路由安全默认值) · [性能对比](#性能对比) · [完整文档](docs/README.md)

## 安装与创建项目

需要 Go 1.26.9 或更新的兼容安全补丁版本，并将 Go 的可执行文件安装目录加入 PATH：

```bash
go install github.com/zhuhanxin0308/thinkgo/v3/cmd/thinkgo@v3.0.2
thinkgo create my-project
cd my-project
go test ./...
thinkgo list
thinkgo build linux/amd64
```

`create` 在当前目录下生成指定项目目录，包含 HTTP 和 CLI 入口、默认 index 应用、配置 JSON、语言包、模板、静态资源及测试。目标必须不存在或为空；隐藏文件和子目录也会使创建被拒绝。进入项目后可用 `thinkgo run` 启动服务，或用 `thinkgo make:app admin` 添加应用。

`go get github.com/zhuhanxin0308/thinkgo/v3@v3.0.2` 用于已有 Go 项目添加依赖；安装 CLI 使用上面的 `go install`。下游项目直接依赖公开版本，不需要克隆框架或添加本地 `replace`。

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

## 路由安全默认值

ThinkGo 默认关闭自动路由、开启强制路由。`config/route.json` 中的 `url_route_must` 缺省为 `true`，新项目也显式采用该配置：

```json
{"url_route_must": true}
```

控制器注册不等于公开其所有动作；业务入口应按 HTTP 方法显式注册路由。未命中规则时不再按 URL 自动调用控制器动作，没有显式 MISS 等接管时返回 404。强制路由不等于 `route_complete_match`，也不替代认证、授权和 CSRF。

已有项目显式配置的 `url_route_must=false` 或注册期调用 `EnableAutoRoute(true)` 仍是主动启用自动调度，不会被自动重写。采用安全默认策略时，应删除旧的显式 `false` 或改为 `true`，并核对路由加载器。完整行为与边界见[自动路由](docs/路由/自动路由.md)。本节描述当前源码策略，已发布版本以对应版本实现为准。

## 性能对比

以下为维护者提供的 **2026-09-24—25** 八框架测试快照。被测 ThinkGo 是 **v3.0.2 / `2797c83b`**，不是后续修复提交的重新测量结果。

[八框架完整业务数值表](docs/benchmarks/2026-09-24/RESULTS.md) · [测试条件与指标口径](docs/benchmarks/2026-09-24/METHODOLOGY.md) · [来源与图表导入状态](docs/benchmarks/2026-09-24/SOURCE.md)

固定 64 VU，博客、商城、社区采用相同 API、业务规则及原生 SQL。吞吐是三轮成功请求/秒的中位数，P99 是各轮 P99 的中位数。应同时比较吞吐和尾延迟，不以单一指标作跨场景总排名。

| 框架 | 博客请求/秒 | 博客 P99（ms） | 商城请求/秒 | 商城 P99（ms） | 社区请求/秒 | 社区 P99（ms） |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Gin | 3,253 | 232.84 | 10,321 | 29.51 | 7,567 | 25.66 |
| Fiber | 3,338 | 224.83 | 11,024 | 27.64 | 8,049 | 23.86 |
| Echo | 3,281 | 222.83 | 10,837 | 28.10 | 7,718 | 24.99 |
| Chi | 3,240 | 236.62 | 10,127 | 30.47 | 7,577 | 25.83 |
| Beego | 3,289 | 225.12 | 10,300 | 29.66 | 7,513 | 26.07 |
| ThinkGo | 3,251 | 227.81 | 9,255 | 32.45 | 7,134 | 26.75 |
| ThinkPHP | 3,014 | 223.64 | 7,253 | 41.16 | 5,918 | 30.23 |
| Laravel | 2,489 | 55.37 | 3,270 | 34.41 | 2,886 | 35.61 |

测试环境为 WSL2 / Intel Core i5-12600KF、本机 HTTP/1.1 长连接和预热的 PostgreSQL 18.6。应用使用 4 逻辑 CPU / 2 GiB；Go 单进程 `GOMAXPROCS=4`，ThinkPHP 为 Swoole 8 worker + JIT，Laravel 为 FrankenPHP 16 worker + JIT。不同运行模式及其他资源配置详见测试条件；结果不能外推为公网、TLS、冷缓存、ORM 或生产容量结论。

**图表导入尚未完成：本分支先提交数值表、测试条件和来源说明，原图包的 17 张图及深浅主题 PNG/SVG 资源尚未入库。** 当前没有放置指向缺失图片的链接；完整图集导入、哈希校验及 README 实际显示验证完成前，文档合并请求保持 Draft。

## 发布与安全

v3 使用根标签 `v3.X.Y`，模块路径以 `/v3` 结尾。原有 v1、v2 标签保留；升级时需要同步修改依赖和 import 路径。发布工作流执行双 CGO 测试、Race Detector、逐包 80% 覆盖率、路由 fuzz、真实性能预算、MySQL/PostgreSQL/Redis/MongoDB/Neo4j 真实服务契约、静态与漏洞扫描，并生成 CycloneDX SBOM、SHA-256 校验和和构建 provenance。发布结果以 [GitHub Releases](https://github.com/zhuhanxin0308/thinkgo/releases) 和对应工作流状态为准，维护者流程见 [RELEASING.md](RELEASING.md)。

框架采用 Apache License 2.0，完整条款见本目录的 `LICENSE`。
