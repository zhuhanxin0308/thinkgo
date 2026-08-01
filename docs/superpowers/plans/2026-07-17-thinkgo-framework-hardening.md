# ThinkGo 框架加固实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task. Each task must follow RED → GREEN → REFACTOR and receive a specification review plus a code-quality review before it is committed.

**Goal:** 在不改变 ThinkGo 现有兼容行为的前提下，修复已确认的架构、安全与性能问题，使框架在没有数据库配置时仍可正常启动和使用，并以可复现测试、竞态检测和基准结果证明改动有效。

**Architecture:** `ApplicationManager` 成为多应用唯一生命周期所有者；请求体保持一次性流语义；Trace、缓存和视图调试数据改为请求私有；日志与语言检测使用不可变快照降低热路径开销；路由冻结时构建只读分段索引并以现有线性匹配器作最终语义校验；Windows 文件会话只在本地文件锁上使用允许删除共享的句柄和有限重试。数据库始终是可选模块，不引入分布式锁，也不改变现有公开 API 的默认语义。

**Tech Stack:** Go 1.26.5、`net/http`、现有 ThinkGo 模块、`golang.org/x/sys/windows`、`github.com/HdrHistogram/hdrhistogram-go v1.3.0`（仅基准命令使用）。

> **当前状态（2026-07-20）：** Task 1–7 已在当前主线实现，并通过对应包级测试、Race、无数据库启动、真实 SQLite 业务链路和全量质量门禁；Task 8–9 的验收条目已标记完成。前面的 `[ ]` 保留为历史 RED/GREEN 实施记录，不应单独解读为当前代码未实现。当前主线另增加了可选的 Memory Cache `max_entries` FIFO 容量治理、有界并行数据库初始化和连接错误脱敏。正式 GA 仍受 `RELEASING.md` 规定的许可证、制品签名、外部安全审计和生产容量证据约束。

## 全局约束与完成标准

- 所有新增或修改的 Go 注释使用中文；公开 API 注释明确生命周期、并发和错误边界。
- 每个修复先提交能准确复现问题的失败测试，确认失败原因与目标缺陷一致，再编写实现；禁止先写实现后补测试。
- 缺少 `config/database.json` 表示未启用数据库，启动不得失败；显式存在但格式错误的数据库配置仍必须报告启动错误；连接失败保持当前“记录警告但不阻断框架启动”的兼容行为。
- 不引入数据库锁、Redis 锁、租约或任何分布式锁；Windows Session 修复仅作用于单机文件锁。
- 保留旧 API 和默认行为：`PipeByName` 未命中仍静默、`Cookie.ForRequest` 与 `Session.NewRequestSession` 保持旧判断、日志队列默认溢出同步写、PostgreSQL 默认项不变、路由 HEAD/OPTIONS/405/ANY/域名/正则/可选参数语义不变。
- 性能改动必须同时满足语义对照测试和基准证据；若路由索引或其他优化在相应规模出现稳定回退，则不得保留该优化。
- 单元测试应覆盖有效、无效、边界和并发场景；目标业务覆盖率不低于 80%，不编写只为命中行数的测试。
- 源文件只通过 `apply_patch` 逐文件编辑；`gofmt` 仅用于格式化 Go 文件。每个任务只暂存本任务文件，执行 `git diff --cached --check` 后独立提交。
- 文档只在代码、测试和性能结论稳定后更新；文档描述最终真实行为，不编写阶段总结或占位章节。

---

## Task 1：建立“数据库可选”的启动契约

**Files:**

- Modify: `framework/app.go`
- Modify: `framework/app_database_hardening_test.go`
- Add: `framework/http/multi_app_no_database_test.go`
- Verify: `framework/app_database_env_test.go`
- Verify: `framework/db/manager.go`

### 1.1 RED：复现没有数据库配置时启动失败

- [ ] 在 `framework/app_database_hardening_test.go` 增加 `TestAppStartsWithoutDatabaseConfiguration`。使用项目测试夹具创建最小应用后删除生成的 `config/database.json`，断言：
  - `BuildApp` 返回已初始化 App；需要观察启动错误时使用 `NewAppUninitialized` 并显式调用 `Initialize`；
  - `StartupError()` 为 `nil`；
  - `ServiceDB` 不可解析；
  - `ServiceDBManager` 非空且默认连接查询不会制造连接；
  - 非数据库模块至少完成一次缓存 `Set/Get` 或等价的真实可用性验证。
- [ ] 增加“数据库配置有效但对应驱动未注册”的兼容测试，断言只记录连接初始化警告、`StartupError()` 仍为 nil，框架其他模块可用。
- [ ] 在 `framework/http/multi_app_no_database_test.go` 删除各应用数据库配置，通过 `ApplicationManager.Boot` 与 `MultiHttp` 完成一次真实非数据库 HTTP 请求，断言 200、响应体正确且关闭顺序正常。
- [ ] 保留并扩充现有显式错误配置测试：文件存在但 `default` 为空、类型错误或连接定义缺失时，`StartupError()` 必须非空。
- [ ] 运行：

```powershell
go test ./framework ./framework/http -run 'Test(AppStartsWithoutDatabaseConfiguration|Database.*Hardening|MultiHttpRunsWithoutDatabase)' -count=1
```

预期：无数据库配置测试因 `readDefaultDatabaseConnection` 返回错误而失败；显式错误配置测试保持原有结果。

### 1.2 GREEN：仅在配置真实存在时初始化数据库

- [ ] 在 `App.initialize` 的数据库阶段先读取 `ServiceConfig` 对应配置的 `Has("database")`。只有根配置存在时才调用 `readDefaultDatabaseConnection` 和 `initDatabaseConnections`。
- [ ] 无数据库配置时使用稳定的内部默认管理器名称创建 `db.Manager`，但不创建连接、不记录启动错误；该默认名称定义为常量，避免散落字符串。
- [ ] 显式存在的空对象或错误结构继续走严格校验并记录启动错误；有效配置但连接不可达继续保留当前警告且不中止启动。
- [ ] 不改变环境变量覆盖、连接池默认值、驱动选择或 PostgreSQL 默认参数。

目标控制流固定为：

```go
databaseConfigured := cfg.Has("database")
defaultConnection := defaultDatabaseConnectionName
var databaseConfigErr error

if databaseConfigured {
	configuredDefault, err := readDefaultDatabaseConnection(databaseConfig)
	databaseConfigErr = err
	if databaseConfigErr != nil {
		app.recordStartupError(databaseConfigErr)
	} else {
		defaultConnection = configuredDefault
	}
}

app.dbManager = db.NewManager(defaultConnection)
if databaseConfigured && databaseConfigErr == nil && !app.skipDatabaseInit {
	app.initDatabaseConnections(databaseConfig, defaultConnection)
}
```

实现时不得直接用全局 `StartupError() == nil` 屏蔽数据库之前无关阶段的既有行为；应保存本阶段的数据库配置校验结果，只以该结果控制连接初始化。

### 1.3 验证与提交

- [ ] `gofmt -w framework/app.go framework/app_database_hardening_test.go framework/http/multi_app_no_database_test.go`
- [ ] `go test ./framework ./framework/http -run 'Test(AppStartsWithoutDatabaseConfiguration|Database|MultiHttpRunsWithoutDatabase)' -count=1`
- [ ] `go test ./framework/... -count=1`
- [ ] 暂存本任务修改文件，执行 `git diff --cached --check`。
- [ ] 提交：`fix: allow startup without database configuration`

---

## Task 2：恢复一次性请求体语义并统一多应用生命周期

**Files:**

- Modify: `framework/http/multi_app.go`
- Modify: `framework/http/multi_app_test.go`
- Modify: `framework/application_manager.go`
- Modify: `framework/application_manager_test.go`
- Verify: `framework/context/request.go`
- Verify: `framework/http/http.go`

### 2.1 RED：锁定请求体流和生命周期问题

- [ ] 新增 `TestCloneApplicationRequestDoesNotReadBody`，使用会记录 `Read` 次数并可主动报错的 `io.ReadCloser`。调用 clone 后断言克隆过程读取次数为零、克隆请求与原请求持有同一一次性流；测试不得再断言原始 Body 可被二次读取。
- [ ] 新增 `TestMultiHttpRejectsUnknownLengthOversizedBodyWith413`：`ContentLength=-1`，流内容超过应用 `max_body_size`，通过真实 `ServeHTTP` 断言 413。
- [ ] 新增 `TestMultiHttpForwardsSmallBodyExactlyOnce`：控制器读取小请求体并回显摘要，断言底层流只被消费一次且字节完全一致。
- [ ] 新增并发隔离测试：多个应用、多个并发流、不同内容，断言没有串流、提前读取或数据竞态。
- [ ] 在 `framework/application_manager_test.go` 增加：
  - `Close` 在 `booting`、`runPending`、`running` 三种状态都返回稳定状态错误；
  - `Run` 拒绝并发调用；
  - Kernel 错误与逆序关闭错误通过 `errors.Join` 同时保留；
  - 启动顺序仍按应用名稳定排序，关闭顺序仍为逆序。
- [ ] 在 `framework/http/multi_app_test.go` 增加 `TestMultiHttpRunUsesApplicationManagerLifecycle`，用阻塞 Kernel/Host 证明运行期间管理器处于 running，重复 Run 和 Close 都被拒绝。
- [ ] 增加无睡眠端到端健康测试：测试 `runHost` 内创建 `httptest.Server(host)`，通过真实本地 TCP HTTP 请求访问健康路由，再关闭服务器并返回；整个调用从 `MultiHttp.Run` 进入 `ApplicationManager.Run`，不依赖数据库或外部网络。
- [ ] 增加 Provider 在 `Boot` 阶段注册路由的回归测试，断言 `MultiHttp.Run` 必须先完成全部 Provider Boot，再 Freeze，最终该路由可通过 HTTP 命中；提前 Freeze 应使该测试以 `ErrRouterFrozen` 失败。
- [ ] 分别运行以下测试并确认失败落在 `io.ReadAll` 和管理器状态窗口：

```powershell
go test ./framework/http -run 'Test(CloneApplicationRequest|MultiHttp.*Body|MultiHttpRunUses)' -count=1
go test ./framework -run 'TestApplicationManager(Close|Run)' -count=1
```

### 2.2 GREEN：clone 只复制元数据，Body 交给目标应用消费

- [ ] 删除 `cloneApplicationRequest` 中预读、关闭、恢复和双份复制 Body 的逻辑。使用 `request.Clone(ctx)` 复制 Header 和上下文，显式复制 URL 值，Body 保留同一个一次性流。
- [ ] 不在 MultiHttp 层自行实现大小限制；继续由目标应用已有 `WithMaxBodyBytes`、`Request.Parse` 和 413 映射处理未知长度流。
- [ ] 保留 Host、RequestURI、RemoteAddr、TLS、Trailer 和转发路径改写的既有语义。

目标形状保持现有函数签名和路径改写，只删除请求体复制：

```go
func cloneApplicationRequest(raw *http.Request, resolution framework.ApplicationResolution) (*http.Request, error) {
	if raw == nil || raw.URL == nil {
		return nil, fmt.Errorf("%w: 原始请求不能为空", framework.ErrInvalidApplicationRequest)
	}
	cloned := raw.Clone(raw.Context())
	urlCopy := *raw.URL
	cloned.URL = &urlCopy
	cloned.Body = raw.Body
	cloned.GetBody = nil
	cloned.URL.Path = resolution.RewrittenPath
	cloned.URL.RawPath = ""
	cloned.RequestURI = cloned.URL.RequestURI()
	return cloned, nil
}
```

### 2.3 GREEN：ApplicationManager 成为唯一生命周期所有者

- [ ] 给管理器增加受同一互斥锁保护的 `runPending` 状态，消除 `Boot` 完成到 `running=true` 之间可被 `Close` 穿过的窗口。`runPending` 只由 `Run` 拥有，私有 boot 逻辑不得清理它。
- [ ] 将公开 `Boot()` 委托给私有 `boot(fromRun bool)`：普通 Boot 在 `runPending` 时拒绝，`Run` 自己设置 `runPending` 后允许进入同一套 Boot 逻辑；boot 只负责设置和清理 `booting`。
- [ ] `Run` 在持锁状态校验 closed/booting/runPending/running 后设置 `runPending`。Boot 失败时由 Run 清除 `runPending`；Boot 成功时必须在同一次持锁临界区原子执行 `runPending=false`、`running=true`。Kernel 返回后清理 running，再调用 Close，并用 `errors.Join` 保留两类错误。
- [ ] `Close` 在 booting/runPending/running 任一状态为真时拒绝，避免关闭正在初始化或服务中的应用。
- [ ] `MultiHttp.Run` 只调用一次 `manager.Run`；适配 Kernel 在管理器完成 Boot 后依次执行 `freezeRoutes`、校验 `runHost`、运行宿主。删除手工 Boot/Run/Close 拼接，使冻结失败、宿主缺失和运行错误都由管理器统一逆序关闭并合并错误。
- [ ] 为私有函数适配器增加中文注释：

```go
type multiHTTPKernel func() error

func (kernel multiHTTPKernel) Run() error {
	return kernel()
}
```

- [ ] 冻结或宿主校验失败时仍关闭已经启动的应用并合并错误，但不得绕过管理器状态机。

### 2.4 验证与提交

- [ ] `gofmt -w framework/http/multi_app.go framework/http/multi_app_test.go framework/application_manager.go framework/application_manager_test.go`
- [ ] `go test ./framework/http -run 'Test(CloneApplicationRequest|MultiHttp)' -count=1`
- [ ] `go test ./framework -run 'TestApplicationManager' -count=1`
- [ ] `go test -race ./framework ./framework/http -count=1`
- [ ] 暂存本任务文件并执行 `git diff --cached --check`。
- [ ] 提交：`fix: stream multi-app requests through managed lifecycle`

---

## Task 3：修复 Windows 文件 Session 的删除共享竞争

**Files:**

- Modify: `framework/session/driver/file.go`
- Add: `framework/session/driver/file_lock_windows.go`
- Add: `framework/session/driver/file_lock_other.go`
- Modify: `framework/session/driver/file_test.go`
- Modify: `framework/session/driver/file_error_test.go`
- Add: `framework/session/driver/file_lock_windows_test.go`
- Add: `framework/session/driver/file_process_test.go`
- Verify: `framework/session/session_concurrent_test.go`
- Verify: `framework/session/session_gc_test.go`

### 3.1 RED：复现跨实例与跨进程竞争

- [ ] 使用两个独立 `File` 实例指向同一目录，增加并发读写/销毁测试，避免只验证同一个 Go 对象内的互斥锁。
- [ ] Windows 专属测试打开锁文件句柄后并发执行删除和重建，断言允许删除共享的句柄不会因普通共享冲突永久失败。
- [ ] 增加共享冲突有限重试测试：仅 `ERROR_SHARING_VIOLATION`/等价可识别错误进入重试，非共享错误立即返回；重试达到上限返回带路径与操作的错误。
- [ ] 增加 owner/identity 替换测试：等待期间锁文件被另一拥有者替换时，旧拥有者不得删除新锁。
- [ ] 使用测试子进程并发增加同一 Session 计数，断言最终值准确、JSON/信封不损坏、无遗留锁文件。
- [ ] 运行：

```powershell
go test ./framework/session/driver -run 'TestFile(SessionConcurrent|Lock|Owner|Process)' -count=1
```

预期：现有普通 `os.OpenFile` 句柄在 Windows 删除共享场景失败或暴露非原子窗口。

### 3.2 GREEN：按平台封装本地锁文件操作

- [ ] `file.go` 只调用内部 `createLockFile`、`openLockFile`、`removeOwnedLockFile`，不包含平台判断；普通 `.session` 数据文件继续使用现有快速路径。
- [ ] `file_lock_other.go` 使用 `//go:build !windows`，保持当前 Unix `os.OpenFile`/`os.Remove` 行为，不增加重试。
- [ ] `file_lock_windows.go` 使用 `windows.CreateFile`，共享标志固定为 `FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE`，创建语义分别对应 `CREATE_NEW` 和 `OPEN_EXISTING`，然后转换成 `*os.File`。
- [ ] 将重试上限、初始退避和最大退避定义为命名常量；只对共享冲突退避，单次总等待必须有明确上界，不得无限循环。
- [ ] 每次重试前重新读取锁 identity/owner；只有仍属于当前 owner 时才允许删除。发现锁已被替换时视为所有权丢失，不删除新锁。
- [ ] 对底层错误包装 `*os.PathError`，保留 `errors.Is` 可判断性。
- [ ] 不增加网络、数据库或 Redis 依赖，不实现分布式锁。

### 3.3 验证与提交

- [ ] `gofmt -w framework/session/driver/file.go framework/session/driver/file_lock_windows.go framework/session/driver/file_lock_other.go framework/session/driver/file_test.go framework/session/driver/file_error_test.go framework/session/driver/file_lock_windows_test.go framework/session/driver/file_process_test.go`
- [ ] `go test ./framework/session/driver ./framework/session -count=20`
- [ ] `go test -race ./framework/session/driver ./framework/session -count=1`
- [ ] 在 Windows 之外再做一次非 Windows 条件编译验证，确认 `!windows` 快路径没有符号或 build tag 问题：

```powershell
$env:GOOS='linux'
go test -c -o $env:TEMP\thinkgo-session-linux.test ./framework/session/driver
Remove-Item -LiteralPath $env:TEMP\thinkgo-session-linux.test
Remove-Item Env:GOOS
```
- [ ] 暂存本任务文件并执行 `git diff --cached --check`。
- [ ] 提交：`fix: make windows file session locking deletion-safe`

---

## Task 4：把 Trace、缓存和视图调试数据限制在单个请求内

**Files:**

- Modify: `framework/debug/debug.go`
- Modify: `framework/debug/debug_test.go`
- Modify: `framework/constants.go`
- Modify: `framework/middleware/trace.go`
- Modify: `framework/middleware/trace_test.go`
- Modify: `framework/cache/cache.go`
- Modify: `framework/cache/cache_feature_test.go`
- Modify: `framework/view/view.go`
- Modify: `framework/view/view_test.go`
- Modify: `framework/controller.go`
- Add: `framework/controller_debug_test.go`
- Modify: `framework/app.go`
- Modify: `framework/app_cache.go`

### 4.1 RED：证明全局调试对象泄漏与无界增长

- [ ] 在 `framework/debug/debug_test.go` 为现有日志、SQL、缓存、变量和文件集合建立超限测试，断言长度不超过命名上限并能查询是否截断；重复文件记录保持 O(1) 去重语义。
- [ ] 在 Trace 测试中并发发送带不同缓存键和模板名的本地请求，断言每个响应面板只包含自己的数据；远端请求、Trace 关闭请求不得创建或挂载 collector。用 `testing.AllocsPerRun` 或等价基准对照证明禁用 Trace 不产生 collector 额外堆分配。
- [ ] 在 Cache 测试中断言 `root.WithDebug(a)` 与 `root.WithDebug(b)` 共享驱动和 Store 状态但调试记录完全隔离，root 未绑定 collector；Tag/Store 返回的 facade 必须继续绑定同一个请求 collector。
- [ ] 在 View/Controller 测试中断言 `RenderWithDebug`/控制器视图渲染把模板记录写入当前请求 collector，旧 `Render`/`Fetch` 仍写入构造时传入的 collector。
- [ ] 运行：

```powershell
go test ./framework/debug ./framework/middleware ./framework/cache ./framework/view ./framework -run 'Test(DebugBound|Trace.*Isolation|CacheWithDebug|View.*Debug|Controller.*Debug)' -count=1
```

预期：边界 API 尚不存在，且旧的应用级共享调试状态造成请求数据共享。

### 4.2 GREEN：有界 collector 与统一请求键

- [ ] 在 `framework/debug` 定义 `RequestKey = "_debug"`，并提供：

```go
func FromRequest(request *context.Request) *Debug
type Kind string

const (
	KindLog   Kind = "log"
	KindSQL   Kind = "sql"
	KindCache Kind = "cache"
	KindVar   Kind = "var"
	KindFile  Kind = "file"
)

func (debug *Debug) Truncated(kind Kind) bool
```

- [ ] `framework.DebugRequestKey` 保留为兼容别名，值引用 `debug.RequestKey`，避免两个包继续散落硬编码。
- [ ] 每类记录使用命名上限；日志、SQL、缓存和文件达到上限后不继续增长，变量 map 达到键数量上限后拒绝新键但允许更新已有键，所有类别都设置截断标记。文件去重改为内部 set，返回快照时再复制，禁止调用方修改内部集合。
- [ ] `Debug.Enabled` 保持现有公开字段类型和构造语义，不改成破坏兼容的原子类型；应用启动完成后按不可变配置使用。

### 4.3 GREEN：请求私有 Trace 和调试 facade

- [ ] Trace 中间件只在 `Enabled()` 且请求来自允许的本地来源时创建 collector；未授权请求不设置请求键、不分配 collector。
- [ ] collector 生命周期由请求拥有，处理结束后不写回应用级共享状态，不使用 goroutine-local 或“当前请求”全局变量。
- [ ] 从 `cacheState` 移除 debug 字段，把 collector 放到轻量 `Cache` facade：

```go
func (cache *Cache) WithDebug(collector *debug.Debug) *Cache
```

  该方法复制 facade、共享底层 state/driver；`Store`、`Tag` 和 TaggedCache 操作传播 facade collector。`NewCache` 的旧 trace 参数仍构造绑定 collector 的 facade，保持直接调用兼容。
- [ ] 应用根缓存使用 `nil` collector；给控制器增加 `RequestCache() *cache.Cache`，返回根缓存的 `WithDebug(debug.FromRequest(c.Request))` facade。
- [ ] View 新增：

```go
func (view *View) RenderWithDebug(collector *debug.Debug, writer io.Writer, name string, data map[string]interface{}) error
func (view *View) FetchWithDebug(collector *debug.Debug, name string, data map[string]interface{}) (string, error)
```

  旧 `Render`/`Fetch` 调用新方法并传入 `view.debug`；App 根 View 使用 nil；`Controller.View` 显式传入当前请求 collector。
- [ ] 调试页渲染只读取 collector 快照，并显示各类别是否截断；模板不得直接迭代可变内部切片。

### 4.4 验证与提交

- [ ] `gofmt` 本任务全部 Go 文件。
- [ ] `go test ./framework/debug ./framework/middleware ./framework/cache ./framework/view ./framework -count=1`
- [ ] `go test -race ./framework/debug ./framework/middleware ./framework/cache ./framework/view ./framework -count=1`
- [ ] 暂存本任务文件并执行 `git diff --cached --check`。
- [ ] 提交：`fix: isolate and bound request trace data`

---

## Task 5：优化日志与语言检测热路径

**Files:**

- Modify: `framework/log/log.go`
- Modify: `framework/log/log_test.go`
- Modify: `framework/app_log.go`
- Add: `framework/app_log_hardening_test.go`
- Modify: `framework/lang/lang.go`
- Modify: `framework/lang/lang_test.go`
- Modify: `framework/lang_middleware.go`
- Modify: `framework/lang_middleware_test.go`

### 5.1 RED：日志过滤发生在格式化之后

- [ ] 增加 `IsLevelEnabled` 语义测试：大小写规范化、未知级别、空级别列表、并发 `SetLevels` 与写日志都可预测且无竞态。
- [ ] 使用实现 `fmt.Stringer` 的计数对象测试禁用的 `Infof`/`Logf` 不调用 `String()`，证明过滤早于格式化。
- [ ] 增加访问日志禁用 Info 时的测试，使用可观察请求字段/基准分配数证明不构造 path/IP/字段 map。
- [ ] 增加默认队列满时同步写测试；新增显式 drop 策略测试，断言丢弃计数精确递增；无效策略和启动后修改策略返回稳定错误。
- [ ] 运行：

```powershell
go test ./framework/log ./framework -run 'Test(LogLevelSnapshot|DisabledFormattedLog|Overflow|AccessLogDisabled)' -count=1
```

### 5.2 GREEN：原子不可变级别快照与显式溢出策略

- [ ] `SetLevels` 构造规范化的不可变级别集合并通过 `atomic.Pointer`/`atomic.Value` 一次发布；读取不加 RWMutex、不线性 `EqualFold`。
- [ ] 所有格式化日志方法先调用 `IsLevelEnabled`，禁用时立即返回；普通字段日志也在复制字段前过滤。
- [ ] API 形状固定为：

```go
type OverflowPolicy uint8

const (
	OverflowSync OverflowPolicy = iota
	OverflowDrop
)

func (log *Log) IsLevelEnabled(level string) bool
func (log *Log) SetOverflowPolicy(policy OverflowPolicy) error
func (log *Log) DroppedEntryCount() uint64
```

- [ ] 默认 `OverflowSync` 完全保持现有行为；只有显式 `OverflowDrop` 才非阻塞丢弃并用原子计数记录。策略在 worker 启动前固定，避免运行期竞态。
- [ ] `app_log.go` 支持可选 `overflow_policy: sync|drop`；缺失时为 sync，非法值记录配置启动错误，不能静默切换。
- [ ] `writeAccessLog` 第一行检查日志服务的 `IsLevelEnabled(log.LevelInfo)`，禁用时不读取或构造后续字段。

### 5.3 RED/GREEN：语言检测快照

- [ ] 建立表驱动兼容测试，固定优先级：query → cookie → header → Accept-Language → default；覆盖空值、未知值、前缀匹配、允许语言顺序、现有 qvalue 行为与 public `DetectionConfig` 防御性复制。
- [ ] 增加并发更新配置/读取快照测试；请求检测只读取一次快照，不得在同一请求中混用两个版本。
- [ ] 增加检测基准，记录 `BenchmarkDetectLanguage` 的 ns/op 与 allocs/op 基线。
- [ ] 在 Lang 内部发布不可变 detection snapshot，预计算：标准化 allowed set、前缀索引、稳定顺序、默认语言和各来源开关。
- [ ] 增加一次性入口：

```go
func (lang *Lang) DetectLanguage(queryValue, cookieValue, headerValue, acceptLanguage string) string
```

- [ ] 跳过空来源；前缀匹配不得每请求排序/复制 allowed 列表；`DetectionConfig()` 继续返回防御性副本。
- [ ] 中间件一次提取四类值并调用 `DetectLanguage`，不逐来源重复读取配置。
- [ ] 不借本次优化收紧 qvalue 或其他既有匹配语义。

### 5.4 验证与提交

- [ ] `gofmt` 本任务全部 Go 文件。
- [ ] `go test ./framework/log ./framework/lang ./framework -run 'Test(Log|AccessLog|Lang|DetectLanguage)' -count=1`
- [ ] `go test -race ./framework/log ./framework/lang ./framework -count=1`
- [ ] `go test ./framework/log ./framework/lang -run '^$' -bench 'Benchmark(Log|DetectLanguage)' -benchmem -count=5`
- [ ] 暂存本任务文件并执行 `git diff --cached --check`。
- [ ] 提交：`perf: publish immutable log and language snapshots`

---

## Task 6：补齐中间件、代理和启动安全边界

**Files:**

- Modify: `framework/middleware/pipeline.go`
- Add: `framework/middleware/pipeline_alias_test.go`
- Modify: `framework/context/request.go`
- Add: `framework/context/request_trusted_proxy_test.go`
- Modify: `framework/http/http.go`
- Modify: `framework/http/http_test.go`
- Modify: `framework/cookie/cookie.go`
- Modify: `framework/cookie/cookie_test.go`
- Modify: `framework/session/session.go`
- Modify: `framework/session/session_test.go`
- Modify: `framework/middleware/session.go`
- Modify: `framework/middleware/session_test.go`
- Modify: `framework/app_security.go`
- Add: `framework/app_security_hardening_test.go`
- Modify: `framework/app.go`

### 6.1 RED：严格中间件别名接口

- [ ] 测试旧 `PipeByName("missing")` 继续静默且不改变管道。
- [ ] 测试新 `PipeByNameStrict("missing")` 返回可被 `errors.Is` 判断的 `ErrMiddlewareAliasNotFound`，已注册别名保持原优先级和执行顺序。
- [ ] App 内部注册 CSRF 等必须存在的内置别名时使用 strict；缺失时进入启动错误，而不是静默丢失安全中间件。

目标 API：

```go
var ErrMiddlewareAliasNotFound = errors.New("middleware alias not found")

func (pipeline *Pipeline) PipeByNameStrict(name string) error
```

### 6.2 RED：只有可信代理可以决定 SSL 与 Secure Cookie

- [ ] 覆盖直接 TLS、非可信来源伪造 `X-Forwarded-Proto: https`、可信 IPv4/IPv6/CIDR、多个 forwarded 值、空 trusted proxy 配置。
- [ ] 通过真实 Session 中间件断言非可信来源不能迫使或取消 Secure Cookie，可信代理后的 HTTPS 请求会设置 Secure。
- [ ] 直接调用旧 `Cookie.ForRequest` 和 `Session.NewRequestSession` 的兼容测试保持不变。
- [ ] 增加 trusted proxies 配置只编译一次的基准/可观察测试，禁止每请求重新解析 IP/CIDR。

### 6.3 GREEN：预编译代理集合，框架显式传递安全结论

- [ ] 在 context 包新增只读 `TrustedProxySet` 和构造函数：

```go
func CompileTrustedProxies(values []string) (*TrustedProxySet, error)
func WithTrustedProxySet(set *TrustedProxySet) RequestOption
```

  现有 `WithTrustedProxies` 保留并委托编译函数，保持公开兼容。
- [ ] `parseServerConfig` 启动时编译一次，`serverConf` 保存集合；请求热路径只做 IP/CIDR 命中，不解析配置字符串。
- [ ] `Request.IsSsl()` 仍只在直连 TLS 或 RemoteAddr 命中可信集合时接受 forwarded proto。
- [ ] Cookie 新增显式入口 `ForRequestWithSecure(request, writer, secure bool)`；Session 新增 `NewRequestSessionWithSecure(request, writer, secure bool)`。旧入口调用原有判断以保持直接调用兼容。
- [ ] 框架 Session 中间件调用显式入口并传入 `request.IsSsl()`，使 Secure 决策统一服从可信代理策略。

### 6.4 RED/GREEN：生产安全警告

- [ ] 测试 production 环境下空应用密钥/Session 密钥、空 `allowed_hosts`、CSRF 关闭会各记录一次分类警告；警告不得包含密钥值，不得阻断启动。
- [ ] development/test 环境不输出生产警告；环境判断只用于本警告，不能改变现有 `App.Environment()` 兼容语义。
- [ ] 安全警告环境优先级固定为显式 `APP_ENV` → `app.app_env` 配置 → production；把字符串定义为常量。
- [ ] 警告按“App 实例 + 固定警告类别”去重；每个 App 使用固定类别位图或定长状态，不使用进程级无界 map。这样同一应用不会重复刷屏，不同应用的同类风险仍各自带应用名字段输出一次。

### 6.5 验证与提交

- [ ] `gofmt` 本任务全部 Go 文件。
- [ ] `go test ./framework/middleware ./framework/context ./framework/http ./framework/cookie ./framework/session ./framework -run 'Test(PipeByName|TrustedProxy|SecureCookie|SecurityWarning|CSRF)' -count=1`
- [ ] `go test -race ./framework/middleware ./framework/context ./framework/http ./framework/cookie ./framework/session ./framework -count=1`
- [ ] 暂存本任务文件并执行 `git diff --cached --check`。
- [ ] 提交：`fix: enforce trusted proxy and middleware security boundaries`

---

## Task 7：冻结时构建只读路由分段索引

**Files:**

- Modify: `framework/route/route.go`
- Modify: `framework/route/route_match.go`
- Add: `framework/route/route_index.go`
- Add: `framework/route/route_index_test.go`
- Modify: `framework/route/route_perf_test.go`
- Verify: `framework/route/route_features_test.go`
- Verify: `framework/route/route_registration_test.go`
- Verify: `framework/route/auto_route_test.go`

### 7.1 RED：建立现有线性匹配器作为语义裁判

- [ ] 在测试包保留一个只用于测试的线性参考匹配入口，基于 Freeze 后已排序的原动态路由切片执行当前算法。
- [ ] 表驱动对照至少覆盖：
  - 静态路由优先；
  - literal/必选参数/可选参数/正则的 specificity；
  - 同 specificity 注册顺序；
  - 精确域名优先于无域名；
  - HEAD 显式路由和 GET 回退；
  - ANY、显式 OPTIONS、自动 OPTIONS、405 与稳定 Allow；
  - optional 消费与跳过、空参数、扩展名剥离、解码后的 `%2F`；
  - 自动路由与 MISS 回退。
- [ ] 增加固定随机种子的属性测试：生成合法路由集合和请求，断言索引候选的最终结果（route identity、params、status、Allow）与线性参考完全一致。
- [ ] 增加 fuzz target，种子来自上述边界；fuzz 只比较语义，不依赖耗时。

### 7.2 RED：记录优化前规模基线

- [ ] 扩展基准为 10/100/1000/10000 条动态路由，每个规模包含首部命中、中部命中、尾部命中、未命中；报告 allocs。
- [ ] 在实现前使用系统临时目录保存基线；本机已提供 `benchstat`，比较使用默认 95% 置信水平（`-alpha 0.05`），临时结果不得提交：

```powershell
go test ./framework/route -run '^$' -bench 'BenchmarkRouteMatch/(10|100|1000|10000)' -benchmem -count=10 | Set-Content -Encoding ascii $env:TEMP\thinkgo-route-before.txt
```

### 7.3 GREEN：按 method/domain 构建不可变 trie

- [ ] 保留现有 `staticRoutes` map；只对动态路由在 `Freeze` 时构建索引。
- [ ] 索引按 method 与 exact-domain/common-domain 分区。节点至少区分 literal、required param、regex param 和 optional 分支；optional 在构建时展开“消费/跳过”两条路径。
- [ ] Freeze 先使用现有 specificity+registration order 稳定排序，再按顺序插入；叶节点候选继续保留该顺序并按 route identity 去重。
- [ ] 请求匹配顺序保持：请求 method → HEAD 的 GET 回退 → ANY；每个 method 内 static → exact domain dynamic → common dynamic。
- [ ] trie 只负责缩小候选集合；optional 消费/跳过或其他分支同时命中多个叶子时，必须把候选按原 `specificity desc + registration order asc` 做全局有序合并并去重，不能只保证单个叶子内部有序。最终仍调用现有 `matchRequestParts` 校验正则、扩展名、参数与边界，避免维护第二套语义。
- [ ] `allowedMethods` 与自动 OPTIONS/405 也通过冻结索引筛选候选，但返回顺序必须与现有结果相同。
- [ ] Freeze 后索引完全只读；注册新路由仍按现有规则拒绝，热路径不加锁、不复制整张候选表。

### 7.4 语义与性能门槛

- [ ] `gofmt` 本任务全部 Go 文件。
- [ ] `go test ./framework/route -count=1`
- [ ] `go test -race ./framework/route -count=1`
- [ ] `go test ./framework/route -run 'TestRouteIndexParity' -fuzz 'FuzzRouteIndexParity' -fuzztime=30s`
- [ ] 重跑 10 次基准并执行：

```powershell
go test ./framework/route -run '^$' -bench 'BenchmarkRouteMatch/(10|100|1000|10000)' -benchmem -count=10 | Set-Content -Encoding ascii $env:TEMP\thinkgo-route-after.txt
benchstat -alpha 0.05 $env:TEMP\thinkgo-route-before.txt $env:TEMP\thinkgo-route-after.txt
```

  1000/10000 路由的尾部命中和未命中 `time/op` 必须在 p≤0.05 时显著改善；10/100 不得出现 p≤0.05 的显著回退；任何规模的 `allocs/op` 不得增加。所有数据必须来自同机、同 Go 版本、同命令和相同 benchmark 名称。
- [ ] 若门槛不满足，使用基准剖析定位并调整索引；仍不满足则回退索引实现，只保留对照测试和报告真实结论，不以“理论更快”为由合入。
- [ ] 暂存本任务文件并执行 `git diff --cached --check`。
- [ ] 提交：`perf: index frozen dynamic routes`

---

## Task 8：让基准工具有界统计延迟并增加框架分层对照

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `cmd/benchmark/main.go`
- Add: `cmd/benchmark/main_test.go`
- Add: `framework/http/multi_app_benchmark_test.go`

### 8.1 RED：复现延迟样本随请求数线性增长

- [x] 将延迟聚合拆为可测试类型，先写测试断言每 worker 使用固定桶结构，不保存每个请求的 `[]time.Duration`。
- [x] 用已知样本测试 p50/p90/p95/p99/max，覆盖多 worker Merge、负延迟、高于最高可跟踪值和 Record 错误；`lowestDiscernibleValue` 只作为精度边界，不把低于它的非负值误判为错误。明确 0 延迟按 0 记录，越界不能静默截断。
- [x] 保留现有命令输出字段和单位，新增内部实现不得改变脚本可消费格式。
- [x] 运行 `go test ./cmd/benchmark -count=1`，确认因 histogram API 未实现而失败。

### 8.2 GREEN：使用 HDR Histogram 聚合

- [x] 引入 `github.com/HdrHistogram/hdrhistogram-go v1.3.0`，依赖只由 `cmd/benchmark` 使用，不进入框架请求运行时。
- [x] 把最低可分辨值、最高可跟踪值和有效数字定义为命名常量；每 worker 持有自己的 histogram，完成后由主 goroutine Merge，避免共享锁。`Merge` 返回被丢弃样本数，必须断言该值为 0，不能当作 error 或忽略。
- [x] Record 越界时返回可观察错误并终止该次基准，不能丢失或夹断样本。
- [x] 删除全量 latency slice 和排序逻辑；吞吐、错误数和响应码统计保持现状。

### 8.3 增加 native/single/MultiHttp 对照基准

- [x] 在 `framework/http/multi_app_benchmark_test.go` 使用相同 handler、请求和 recorder，提供：
  - `BenchmarkNativeHTTP`；
  - `BenchmarkSingleAppHTTP`；
  - `BenchmarkMultiAppHTTP`。
- [x] 每个基准报告 allocs，包含无 Body 健康请求和小 JSON Body 请求；MultiHttp 至少覆盖域名解析和路径应用解析。
- [x] 基准不依赖数据库、外部网络或睡眠，不把日志 I/O 混入核心路由结果。

### 8.4 验证与提交

- [x] `gofmt -w cmd/benchmark/main.go cmd/benchmark/main_test.go framework/http/multi_app_benchmark_test.go`
- [x] `go test ./cmd/benchmark ./framework/http -count=1`
- [x] `go test ./framework/http -run '^$' -bench 'Benchmark(Native|SingleApp|MultiApp)HTTP' -benchmem -count=10`
- [x] `go.mod` 与 `go.sum` 只通过 `apply_patch` 增加 HDR 的直接依赖和已核验校验值；执行 `go mod tidy -diff` 做只读一致性检查。若输出无关依赖变化，先查明原因，不用命令批量改写依赖文件。
- [x] 暂存本任务文件并执行 `git diff --cached --check`。
- [x] 提交：`perf: bound benchmark latency aggregation`

---

## Task 9：更新对应文档并执行全量验收

**Files:**

- Modify: `docs/架构/请求流程.md`
- Modify: `docs/架构/多模块和多应用.md`
- Modify: `docs/基础/配置.md`
- Modify: `docs/基础/开发规范.md`
- Modify: `docs/中间件/管道与生命周期.md`
- Modify: `docs/中间件/Session.md`
- Modify: `docs/中间件/Trace.md`
- Modify: `docs/中间件/中间件安全边界.md`
- Modify: `docs/缓存/缓存基础.md`
- Modify: `docs/数据库/连接数据库.md`
- Modify: `docs/路由/路由匹配.md`
- Modify: `docs/路由/路由冻结与快照.md`
- Modify: `docs/路由/路由参数与约束.md`
- Modify: `docs/安全/安全规范.md`
- Modify: `docs/日志/日志通道与配置.md`
- Modify: `docs/中间件/README.md`
- Modify: `docs/多语言/请求语言检测.md`
- Modify: `docs/中间件/语言中间件.md`
- Modify: `docs/模板/视图.md`
- Modify: `docs/控制器/控制器视图.md`
- Add only if benchmark usage has no suitable home: `docs/基础/性能基准.md`
- Modify only if the new benchmark page is added: `docs/README.md`

### 9.1 文档同步

- [x] 数据库文档明确：配置文件缺失即数据库未启用，框架与非数据库模块正常工作；显式错误配置会报告启动错误；连接不可达的现有兼容行为。
- [x] 请求流程和多应用文档明确 Body 是一次性流，MultiHttp 不预读，大小限制由目标应用处理；ApplicationManager 是唯一生命周期所有者。
- [x] Session 文档说明 Windows 本地文件锁的删除共享、有限重试和 owner 复核，并明确这不是分布式锁。
- [x] Trace/缓存/视图文档展示请求 collector、`RequestCache`、`RenderWithDebug` 以及各类记录上限；禁止建议使用全局当前请求。
- [x] 日志文档说明级别快照、默认 sync 与显式 drop、丢弃计数、配置错误；语言配置文档说明检测优先级但不宣称改变 qvalue。
- [x] 中间件与安全文档同时展示旧 `PipeByName` 兼容接口和新 strict 接口；解释 trusted proxies 才能影响 `IsSsl` 与框架 Session Secure Cookie；列出 production 警告是警告而非启动失败。
- [x] 路由文档描述 Freeze 后只读索引、候选最终仍由既有匹配器校验，以及 HEAD/OPTIONS/405/ANY 等兼容边界。
- [x] 性能基准文档只记录可复现命令、场景、指标解释和最新实测表；不写开发过程总结。

### 9.2 静态检查、覆盖率、竞态与全量测试

- [x] 各任务已用明确文件列表完成 `gofmt -w`；最终仅做只读格式复核与仓库检查：

```powershell
gofmt -d framework/app.go framework/app_database_hardening_test.go framework/application_manager.go framework/application_manager_test.go framework/controller.go framework/controller_debug_test.go framework/constants.go framework/app_cache.go framework/app_log.go framework/app_log_hardening_test.go framework/app_security.go framework/app_security_hardening_test.go framework/lang_middleware.go framework/lang_middleware_test.go framework/http/multi_app.go framework/http/multi_app_test.go framework/http/multi_app_no_database_test.go framework/http/multi_app_benchmark_test.go framework/http/http.go framework/http/http_test.go framework/context/request.go framework/context/request_trusted_proxy_test.go framework/cookie/cookie.go framework/cookie/cookie_test.go framework/session/session.go framework/session/session_test.go framework/session/driver/file.go framework/session/driver/file_lock_windows.go framework/session/driver/file_lock_other.go framework/session/driver/file_test.go framework/session/driver/file_error_test.go framework/session/driver/file_lock_windows_test.go framework/session/driver/file_process_test.go framework/middleware/pipeline.go framework/middleware/pipeline_alias_test.go framework/middleware/session.go framework/middleware/session_test.go framework/middleware/trace.go framework/middleware/trace_test.go framework/debug/debug.go framework/debug/debug_test.go framework/cache/cache.go framework/cache/cache_feature_test.go framework/view/view.go framework/view/view_test.go framework/log/log.go framework/log/log_test.go framework/lang/lang.go framework/lang/lang_test.go framework/route/route.go framework/route/route_match.go framework/route/route_index.go framework/route/route_index_test.go framework/route/route_perf_test.go cmd/benchmark/main.go cmd/benchmark/main_test.go
git diff --check
go vet ./...
```

- [x] 全量单测与竞态：

```powershell
go test -count=1 ./...
go test -race -count=1 ./...
```

- [x] 覆盖率写到系统临时目录，按包检查关键修复模块达到有意义的 80% 以上，并确认全仓没有因新增代码明显下降：

```powershell
go test -coverprofile=$env:TEMP\thinkgo-framework-hardening.cover ./...
go tool cover -func=$env:TEMP\thinkgo-framework-hardening.cover
```

- [x] 运行可用的安全/质量工具；若本机未安装，明确记录“工具不可用”而不是伪造通过：

```powershell
staticcheck ./...
govulncheck ./...
gosec ./...
```

本机未安装 `staticcheck`、`govulncheck` 和 `gosec`，已分别执行可用性检查并如实记录，未伪造工具通过结果。

- [x] 重跑最终基准，至少包含语言、日志、路由、native/single/MultiHttp；HTTP 分层基准以 `-count=10 -benchmem` 重跑，报告中区分延迟与分配。
- [x] 对用户明确要求做独立验收：无 DB 启动、无分布式锁依赖、旧 API 兼容、Windows Session、可信代理、请求隔离、默认日志不丢、路由语义对照。

### 9.3 最终审查与提交

- [x] 完成全局规格与代码质量审查；当前环境未提供 `superpowers:requesting-code-review` 工具，因此按同一清单完成自审，并重新执行相关测试和全量测试。
- [x] 检查 `git diff --stat`、`git diff --check`、`git status --short`，确认没有临时程序、覆盖率文件、基准日志或无关用户文件。
- [x] 暂存文档更新并执行 `git diff --cached --check`。
- [x] 提交文档更新（实际拆分为 `docs: sync framework hardening contracts`、`docs: document reproducible HTTP benchmarks` 和 `docs: mark hardening validation complete`，便于逐步回退）。
- [x] 基于最新命令输出完成验证并向用户提供集成选择；当前环境未提供 `superpowers:verification-before-completion` 与 `superpowers:finishing-a-development-branch` 工具，不自行合并或删除分支。
