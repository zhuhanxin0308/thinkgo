# 审计工作流

仓库保留两份可重复运行的审计辅助工作流。它们使用事件对应的完整提交 SHA，并在 checkout 后核对实际源码身份；源码清单、归档名称和验证证据都记录同一 SHA。

## 运行方式

在 GitHub 的 **Actions** 页面选择下表中的工作流，点击 **Run workflow**，选择要检查的分支或标签。工作流合入默认分支后提供手动入口。

这两份工作流及本说明发生变更时，面向 `main` 的 Pull Request 和 `main` 上的 push 会自动运行检查。PR 使用 GitHub 为该事件生成的合并提交，手动运行和 push 使用事件对应的提交；请以制品中的 `source-ref.txt` 核对实际对象。

| 工作流 | 用途 | 制品保留时间 |
| --- | --- | --- |
| Audit offline environment bootstrap | 校验官方工具链，预取完整模块图及下游消费图，导出源码和离线依赖资料 | 1 天 |
| Audit native offline validation | 在隔离容器中执行干净消费方预检、全量普通测试、指定包竞态检查和全量 vet，导出日志与执行计划 | 7 天 |

## 工具链与离线资料

当前固定 Go **1.27.4**、官方发行包 SHA-256、Action 完整提交和容器基础镜像摘要。两份工作流在下载前检查 `go.mod` 中的 Go 版本；升级版本时，需要一起更新两份文件中的版本、官方校验值及合成消费模块声明。

bootstrap 的三个制品以源码 SHA 命名，分别包含工具链、源码和模块资料。每组都附有 `SHA256SUMS`。模块制品包含原始 `go.mod` / `go.sum`、预取模块图、消费方清单、校验结果、签名树 checkpoint，以及 `modules.tar.gz`。

| `modules.tar.gz` 内路径 | 还原用途 |
| --- | --- |
| `modcache/` | 设为只读 `GOMODCACHE`；`modcache/cache/download` 提供文件模块代理 |
| `gopath/pkg/sumdb/` | 保留 Go 独立维护的公开签名树状态；消费方运行前复制到其专用 `GOPATH/pkg/sumdb` |

`resolved-go.sum` 记录预取后元数据目录的校验清单；归档中的原始 `go.mod` / `go.sum` 保持与源码一致。合成消费模块覆盖框架入口、HTTP、数据库连接、CLI 和 OpenAPI 的依赖解析。增加消费路径或依赖时，应同时检查两份工作流的预取范围。

bootstrap 阶段只进行可信工具链下载、元数据解析及依赖校验，不构建或运行框架程序。native 工作流独立执行相同的预取过程，再以无外网的干净消费方实际验证这些依赖。离线资料的适用范围以记录的源码 SHA 和模块图为准。

## 执行边界与覆盖范围

仓库权限限定为 `contents: read`，checkout 不保留认证信息。联网准备阶段使用公开 Go 模块代理、checksum database 和禁用直接 VCS 的配置；目标代码只在执行阶段的容器内运行。

执行容器使用非 root 用户、只读根目录、只读源码/工具链/模块缓存、空环境白名单、独立网络命名空间且无外网、无 Linux capabilities、禁止提权和禁用共享内存。目标代码的工作目录及缓存位于专用 scratch 文件系统。Docker socket、宿主 HOME、凭证和证据目录不挂载给目标代码。

| 资源 | 限制 |
| --- | --- |
| CPU | 2 核 |
| 内存 | 3 GiB，内存加交换空间总额同为 3 GiB |
| 进程 | 256 |
| scratch 磁盘 | 8 GiB 专用文件系统 |
| 单个目标文件 | 128 MiB |
| 输出日志 | 宿主重定向文件受 128 MiB 上限约束；Docker 后台日志驱动关闭 |
| 容器执行时间 | 1500 秒，终止宽限 15 秒 |

普通测试覆盖 `./...`，在冷缓存和受限 CPU 下给每个包 10 分钟预算；外层容器时限仍然生效。竞态检查保留 `auth`、`binding`、`context`、`middleware`、`session`、`session/driver`、`filesystem`、`ratelimit`、`cache`、`queue/asynq`、`http` 的原有范围，每个包限时 180 秒。`go vet` 覆盖 `./...`。

准备步骤失败时立即停止。普通测试、竞态检查和 vet 分别记录退出码，全部成功才通过；任意失败均使作业失败。作业输出仅显示固定阶段名和数字退出码，完整目标输出保留在受限日志中。独立清理步骤在作业结束时移除遗留容器并卸载 scratch。

## 查看结果

native 制品包含 `source-ref.txt`、源码对象清单、镜像身份、模块图、`test-plan.sh`、`native-validation.log` 和 `exit-code.txt`。常规作业输出也会显示消费方预检、baseline、race、vet 是否完成及各自退出码；超时或提前失败的阶段显示 `not reached`。

这些检查提供额外的隔离执行证据。仓库的跨平台测试、真实数据库契约、覆盖率和发布门禁继续由 **Framework CI and Release** 执行；判断变更是否可以合并时，需要一起检查相关工作流的结果。
