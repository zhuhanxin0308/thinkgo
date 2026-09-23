# 框架发布流程

ThinkGo 只发布根目录框架模块 `github.com/zhuhanxin0308/thinkgo/v3`。框架仓库包含可安装的 `cmd/thinkgo`、文档、测试和发布工具；业务宿主通过 `thinkgo create` 生成。v3 发布标签格式为 `v3.X.Y`，由 `.github/workflows/framework-release.yml` 处理；main 和 pull request 运行同一套检查，但不创建 Release。

本地测试通过只代表本地门禁，不代表标签、GitHub Release、模块代理或下游项目验收完成。

## 发布前置条件

1. 根 `go.mod` 必须声明 `github.com/zhuhanxin0308/thinkgo/v3`，`version/version.go` 的 `Number` 必须与目标模块版本一致。
2. 根 `LICENSE`、`README.md` 和本文件必须存在。
3. 工作树必须干净；仓库不得跟踪 `.env`、证书、私钥或其它凭据。
4. 仓库根不含业务 `app/`、`public/`、`main.go`、`go.work` 或旧 `framework/` 子模块。根 `config/` 是框架 Go 包；测试用 JSON 配置放在 `testdata/project/config/`。
5. 所有配置示例只能包含结构、默认值或无敏感信息的示例值；生产凭据由部署环境注入。

## 本地门禁

在仓库根目录使用 `GOWORK=off`，至少完成：

1. `go mod verify`、`go mod tidy -diff`、`go build ./...` 和 `go vet ./...`；先执行 `go test -run '^TestModuleReleaseArchive$' -count=1 .`，确认文件名、目录和版本能够生成合法 Go 模块归档。
2. Staticcheck、Gosec、Govulncheck 与 Actionlint。
3. `CGO_ENABLED=1` 和 `CGO_ENABLED=0` 两套全量测试。
4. Oracle build tag、Race Detector 和逐包不低于 80% 的有效覆盖率。
5. `TestHTTPHealthAllocationBudgets` 以及 `BenchmarkNativeHTTP`、`BenchmarkSingleAppHTTP`、`BenchmarkMultiAppHTTP` 的符号存在性与真实执行。
6. `FuzzRouteIndexParity`；不能只运行 seed corpus 后宣称 fuzz 通过。
7. `integration` 构建标签下的 MySQL、PostgreSQL、Redis Cache、Redis Session、MongoDB 和 Neo4j 真实服务契约；测试符号必须先校验存在，不能因环境变量缺失跳过后形成假绿。

CI 验证生成器、框架集成、SQLite 业务路径、Windows/macOS 以及全部支持平台的交叉编译。框架 Release 不发布用户业务应用的二进制。

## 创建版本

1. 更新 `version/version.go` 的 `Number`，确认目标标签与源码版本一致。
2. 完成所有门禁并提交，确认 `git status --porcelain` 为空。
3. 创建不可移动的根模块标签，例如：

   ```bash
   git tag -a v3.0.2 -m "ThinkGo Framework v3.0.2"
   git push origin v3.0.2
   ```

   是否使用 `-s` 签名标签由仓库治理策略决定；已经发布的标签不得移动或覆盖，原 v1、v2 标签保留。

4. 等待 Framework CI and Release 工作流完成。工作流会再次验证版本和标签、模块隔离、格式、构建、测试、覆盖率、Race、fuzz、真实性能门禁、真实服务契约、静态与漏洞扫描。
5. 工作流从根模块生成 CycloneDX SBOM，附带 Apache-2.0 许可证、依赖闭包、序列号和生成时间；随后生成 SHA-256 校验和及 GitHub OIDC provenance。
6. 发布前，工作流会在不含本仓库 `go.work` 和本地 `replace` 的临时模块中通过 `GOPROXY=direct` 获取目标模块标签，并运行一个真实导入 `ApplicationDefinition`、注册应用和读取应用清单的下游测试；只完成 `go get` 或 `go list` 不算下游编译验收。

## 发布后验收

1. 确认 GitHub Release 对应 `v3.X.Y` 标签，SBOM、许可证、`SHA256SUMS` 和 provenance 均存在。
2. 在独立目录再次执行：

   ```bash
   go mod init example.com/thinkgo-consumer
   go get github.com/zhuhanxin0308/thinkgo/v3@v3.0.2
   # 写入实际业务入口或测试代码并导入框架公开 API
   go test ./...
   ```

3. 将示例中的版本替换为目标版本，分别验证模块代理与 `GOPROXY=direct`；代理同步可能存在延迟，未能解析目标版本时不能宣称公共发布完成。使用 `go install github.com/zhuhanxin0308/thinkgo/v3/cmd/thinkgo@v3.0.2` 安装同版本 CLI，执行 `thinkgo create release-smoke`，在生成工程内完成 `go test ./...`、`thinkgo build linux/amd64` 和产物的 Docker Compose 配置校验；发布工作流也执行此链路。
4. 下游验收至少覆盖单应用兼容、两个应用的路径/域名分发、错误应用启动阻断、配置与容器隔离、优雅关闭，以及所需数据库和外部服务。
5. 发布门禁中的 MySQL、PostgreSQL、Redis、MongoDB 和 Neo4j 容器用于验证框架基础契约，不代表用户的具体版本、TLS、鉴权、拓扑或容量配置已经验收。SQL Server、Oracle、生产数据库与 Redis、反向代理、文件权限、生产容量、告警和回滚演练仍属于环境验收；本地测试和 GitHub Actions 不能替代这些证据。

## 回滚

发布后发现阻塞问题时停止推广受影响版本，保留原标签、SBOM、校验和与 provenance 用于审计，并发布包含修复的新补丁版本。不得删除、移动或覆盖已经发布的标签。若框架变更影响数据库迁移或持久化格式，必须先验证向后兼容和迁移回退，再调整下游版本。
