# GitLab CI 合并门禁

`.gitlab-ci.yml` 为分支、标签和合并请求提供 Linux 检查。已有合并请求的分支推送只创建合并请求流水线，避免重复运行相同提交。工作流不执行部署、创建发布或自动合并。

## 检查范围

| 作业 | 检查 |
| --- | --- |
| `verify` | Go 格式、模块校验、依赖文件一致性、构建、`go vet`、模块发布归档测试、CI 结果校验器和工作区隔离测试 |
| `unit` | CGO 关闭和开启两种模式下的整仓测试，随机顺序且禁用结果缓存 |
| `race` | 整仓 Race Detector 测试 |
| `coverage` | 逐包 80% 覆盖率；没有可执行语句的包不设门槛 |
| `security` | 固定版本的 Staticcheck、gosec、govulncheck，并阻止旧 openpgp 依赖进入依赖图 |
| `protocol-and-performance` | HTTP 压缩、隐式状态码、CSRF、PostgreSQL 数组与词法回归；分配预算、HTTP 基准、路由和 SQL 词法 fuzz |
| `live-services` | 临时 Redis、MySQL、PostgreSQL、MongoDB、Neo4j 上的契约测试，包括 PostgreSQL 实际解析和参数绑定 |

Go 镜像版本与 `go.mod` 的 `go` 指令保持一致，并设置 `GOTOOLCHAIN=local`，禁止隐式切换工具链。依赖升级必须同步更新镜像版本和缓存前缀。服务镜像沿用 GitHub 发布工作流中已固定的版本与摘要；升级时同步修改两处配置。

所有作业都必须成功，没有 `allow_failure` 或手动跳过的安全检查。报告保存到 `.ci-reports/`，保留七天。性能基准输出供比较，真正自动阻断的性能条件是已有的分配预算测试；不把仅打印基准结果等同于吞吐量验收。

GitHub 的 Windows、macOS、交叉编译、Oracle 标签测试和正式发布流程仍由 `.github/workflows/framework-release.yml` 负责。GitLab Linux 流水线通过不代表跨平台发布矩阵已经通过。

## 验证测试确实执行

`go test -list` 只能确认名称存在，`go test` 返回零也可能包含被跳过的测试。归档、协议回归、分配预算和真实服务门禁同时保存 `go test -json` 的事件流，并通过 `.gitlab/ci/assert_tests.go` 检查必需测试实际出现 `run` 和 `pass`，且所在包已经通过。

校验器拒绝缺失、重复歧义、Skip、失败、缺少结束事件或被截断的 JSON；必需测试的已报告子测试也必须通过。CSRF 的 15 种组合单独列为必需项，因此删除一个组合或在组合中调用 `t.Skip` 不能让门禁通过。普通整仓测试保留既有的条件 Skip 契约，不将非必需的可选平台测试伪装成执行过。

校验器只从标准输入读取 JSON，不接受报告文件路径参数；CI 脚本负责以输入重定向提供自己的报告。直接运行方式为 `go run .gitlab/ci/assert_tests.go TestName < report.jsonl`。这使文件打开与测试结果解析的权限边界分离，避免校验器按命令行输入打开任意路径。

校验器位于隐藏目录，不会被 `go test ./...` 自动发现，`verify` 显式执行它的单元测试和 Race Detector。单元测试、Race 和关键门禁的原始 JSON 日志，以及当前 SHA、Go 和 CGO 配置都会作为报告保存。管道启用 `pipefail`，`tee` 成功不会掩盖测试失败。

## 源码、缓存与报告隔离

`check.sh` 在临时目录创建当前 checkout 的精确 HEAD 提交副本（保留 Git 元数据），所有 Go 检查在该副本执行。原 checkout 中的 `.cache/` 和 `.ci-reports/` 不进入被测模块；报告使用绝对路径写回原 checkout，缓存仍由 GitLab 的既有配置保存。脚本退出时只删除自己创建的临时目录，不清理原源码、缓存或报告。

这样保留 `TestModuleReleaseArchive` 原有的完整目录归档校验，同时避免 Go 编译缓存被计入 500 MiB 模块归档上限，或正在写入的 JSON/日志触发文件大小变化错误。不能通过增大模块归档上限、清空缓存或跳过测试解决该问题。此 CI 脚本验证的是**已提交的 HEAD**，不包含本地未提交的修改；开发期验证未提交修改应直接运行相应 Go 测试。

`verify` 显式执行 `workspace_test.sh`。它使用真实 Git 和 Go，覆盖普通仓库、浅克隆 detached HEAD、带空格路径、未提交文件隔离、500 MiB 稀疏缓存保留、报告写回以及测试失败退出码传播，不使用命令替身。

## Runner 与服务边界

Runner 需要支持 Linux 容器、服务容器、Bash、CGO 的 C 编译器，以及访问 Go 模块代理和所需镜像仓库。`live-services` 同时启动五个服务，应提供足够的 CPU 和内存；服务无法启动时须修复 Runner 资源或镜像拉取问题，不能取消检查来制造通过结果。

服务使用作业网络别名，而不是 `127.0.0.1`。配置中的 `thinkgo-live-password` 等只用于作业内临时数据库，不能复用为生产凭据，也不能把服务地址改为生产环境。TCP 就绪检查只检查端口可用，最终连接、认证和协议正确性由契约测试验证。

PostgreSQL 词法和绑定测试显式设置 `standard_conforming_strings=on`，与扫描器采用的默认服务器语义一致。普通字符串中的反斜杠不是引号转义；需要反斜杠转义时使用 `E'...'`。不要在使用该扫描器的连接中关闭此设置。真实服务器测试只执行无副作用的 `SELECT`；没有配置测试主机时允许单独开发运行跳过，但 CI 的必需结果校验会拒绝这一跳过。

## 流水线尚未创建作业时

REST 的流水线查询可能只返回 `failed`、`started_at=null`、`yaml_errors=null` 和零作业，不能据此认定源码测试失败或 YAML 合法。应取得流水线创建接口的具体错误，再按根因处理。

当创建接口返回 `Identity verification is required in order to run CI jobs` 时，需要触发流水线的账户本人在 GitLab 提示横幅中选择 **Verify my account**，按照页面完成所要求的验证。不要向机器人或 MR 提交验证码、证件、支付资料或访问令牌；修改代码、重试旧作业或取消检查不能完成账户身份验证。

验证完成后，在当前 MR 的 **Pipelines** 页选择 **Run pipeline** 创建新流水线，并检查其 SHA 与 MR 最新源提交一致。应看到实际作业被创建并运行，再检查各作业及报告；仍是零作业时继续查看创建阶段错误，而不是把 Draft 改成 Ready。

官方说明：[GitLab CI 身份验证错误](https://docs.gitlab.com/ci/debugging/#error-identity-verification-is-required-in-order-to-run-ci-jobs)。自管 Runner 也是官方支持的执行方式，但必须由维护者提供并授权，不能擅自新建账户、购买资源或扩大权限。

## 合并前启用项目级限制

代码提交不能替代 GitLab 项目设置。维护者应先确认 Runner 可用、合并请求当前源提交的流水线已完整执行，再在项目 **Settings → Merge requests → Merge checks** 启用 **Pipelines must succeed**，并保持 `main` 分支保护。

合并请求的 Draft 状态、没有流水线、等待 Runner、跳过、取消或失败，都不算验证通过。必须核对流水线关联的提交 SHA，不能沿用旧提交的成功状态。本次代码变更不修改项目权限、分支保护、合并策略或自动合并设置。
