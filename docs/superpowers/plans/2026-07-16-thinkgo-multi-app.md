# ThinkGo 多应用架构升级实施计划

> **当前状态（2026-07-20）：** 多应用运行时已经实现并合入当前主线；当前代码证据以 `framework/application_definition_test.go`、`framework/application_resolver_test.go`、`framework/http/multi_app_test.go`、`framework/http/multi_app_no_database_test.go` 和全量 CI 门禁为准。下方复选框保留为历史实施记录，不能单独作为“未实施”或“已发布”的判断；正式发布条件以 `RELEASING.md` 为准。

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 ThinkGo 中实现与 ThinkPHP 多应用模型一致的单进程多应用运行时：每个应用拥有独立的控制器、中间件、模型、验证器、多语言和路由定义，所有应用共用一个 HTTP/TLS/HTTP3 监听器，并根据域名或 URL 路径选择应用。

**Architecture:** 保留项目根目录作为宿主根目录，在根目录下创建一个 ApplicationManager 管理多个独立 App 实例。每个 App 持有自己的容器、配置、路由、事件、服务提供者和注册表；MultiHttp 只负责宿主监听、应用解析、请求路径改写和转发。应用定义通过显式 Go 注册完成，不进行运行时包扫描。

**Tech Stack:** Go 现有标准库和 ThinkGo 现有 framework、framework/http、framework/console、framework/route、framework/context 组件；使用现有测试框架和 httptest，不引入新的运行时依赖。

## Global Constraints

- 所有新增或修改的 Go 注释使用中文；公开 API 的注释说明应用边界、生命周期和错误行为。
- 应用专属代码只允许位于 app/<应用名>/ 下：controller、middleware、model、validate、lang、route、event、service、listener、subscribe、command 等目录随应用归属；不保留旧的根级 app/controller、app/middleware、app/model、app/validate、app/lang、route 兼容层。
- .env、根级 config、public、runtime 和 framework 是宿主级资源；应用级 config 可以覆盖应用配置，但不能覆盖监听器、TLS、HTTP/3 和宿主安全策略。
- 应用实例之间不得共享可变的容器、配置、路由、事件、服务提供者、语言状态、会话状态或应用注册表；请求级应用信息必须挂在请求上下文中，不能写回共享 App。
- 多应用模式只启动一个宿主监听器；应用初始化和服务提供者启动必须在监听前完成，任一应用失败都不得开始监听。
- 域名应用优先于路径应用；域名应用保留原始 URL 路径，路径应用只剥离应用名前缀后交给应用内部路由；未匹配请求按 app_express 配置处理。
- 先写失败测试，再实现最小完整行为；新增业务场景测试需覆盖成功、配置错误、路径安全、应用隔离、生命周期回滚和并发请求，目标覆盖率为 80% 以上，不编写无意义的覆盖率测试。
- 修改文件必须逐文件使用 apply_patch；不得使用脚本或批量命令编辑源文件，不得用命令移动现有应用代码。格式化命令只用于 Go 格式化，不负责内容迁移。
- 编码前必须确认当前工作区已有改动已经形成可回退的提交；本计划执行时只提交本次功能文件，不擅自提交用户已有的无关文档改动。
- 每个可验证阶段完成后提交一次，提交前只暂存该阶段涉及的文件，并运行 git diff --cached --check。

---

## 1. 建立基线和可回退检查点

**Files:** 无源文件修改；只检查仓库状态。

- [ ] 执行 git status --short、git diff --stat 和 git diff -- docs/superpowers/specs/2026-07-16-thinkgo-multi-app-design.md，确认架构规格提交 9b96e66 已存在，且规格文件没有未提交改动。
- [ ] 执行 go test ./... 记录当前基线；如果基线失败，记录失败包、失败测试和失败原因，后续不得把基线失败误报为本次回归。
- [ ] 在开始任何实现编码前，确认当前工作区的既有文档改动已经由用户提交，或得到用户允许创建“现状检查点”提交；不得把既有文档改动和本次实现混入未经确认的提交。

**Expected verification:** 工作区状态、规格提交和基线测试结果可追溯；若既有改动未形成检查点，则暂停编码并请求用户处理检查点。

## 2. 将全局注册表改造成应用实例注册表

**Files:**

- framework/registry.go
- framework/application_definition.go（新增）
- framework/app.go
- framework/registry_test.go
- framework/application_definition_test.go（新增）

### 2.1 先写失败测试

- [ ] 在 framework/application_definition_test.go 增加两个应用定义，断言定义快照按名称排序、重复名称被拒绝、空名称和路径穿越路径被拒绝。
- [ ] 增加两个独立 App 的注册测试：同名控制器、同一路由加载器和同一中间件注册到不同应用后，快照互不包含对方对象。
- [ ] 增加并发快照测试：一个 goroutine 注册实例内组件，多个 goroutine 读取快照；测试只断言线程安全和快照不可被调用方修改。
- [ ] 保留现有 framework/registry_test.go 的单应用兼容测试，用于锁定旧的包级 API 在单应用模式下仍然可用；测试中明确它不能向多应用管理器自动注入全局状态。

测试中的目标接口形状固定为：

~~~go
type ApplicationDefinition struct {
	Name     string
	Path     string
	Register func(*App) error
}

func RegisterApplication(definition ApplicationDefinition) error
func MustRegisterApplication(definition ApplicationDefinition)
func ApplicationDefinitions() []ApplicationDefinition

func (app *App) RegisterController(name string, controller any) error
func (app *App) RegisterRouteLoader(loader RouteLoader) error
func (app *App) RegisterGlobalMiddleware(handler middleware.Handler) error
~~~

### 2.2 实现独立注册状态

- [ ] 在 ApplicationDefinition 中实现名称规范化、应用路径规范化、路径必须位于项目根目录内的校验；错误中包含应用名或路径，便于 CLI 和启动日志定位。
- [ ] 将 applicationRegistry 从包级可变状态拆成 App 私有字段；注册控制器、路由加载器和全局中间件时只操作当前 App 的锁和映射。
- [ ] 将 snapshotControllers、snapshotRouteLoaders、snapshotGlobalMiddlewares 改成 App 方法；返回深拷贝后的切片或映射，避免调用方修改注册表。
- [ ] 包级注册函数不作为多应用入口使用；单应用应通过当前 `App` 实例注册控制器、路由加载器和全局中间件，多应用管理器不读取包级注册状态。
- [ ] 增加应用级注册器的重复名称、空处理器、无效类型和重复加载器校验；错误返回必须可用 errors.Is 或明确错误类型判断。
- [ ] 在 App 初始化前创建应用级注册表，使 Register 回调可以注册全部组件；初始化过程不得读取另一个 App 的注册表。

### 2.3 验证和提交

- [ ] 运行 gofmt -w framework/registry.go framework/application_definition.go framework/app.go framework/registry_test.go framework/application_definition_test.go。
- [ ] 运行 go test ./framework -run 'Test(ApplicationDefinition|ApplicationRegistry|Registry)' -count=1，确认新增失败测试转绿。
- [ ] 只暂存上述文件，运行 git diff --cached --check，提交 refactor: isolate application registries。

## 3. 为 App 增加应用身份、路径和资源边界

**Files:**

- framework/app.go
- framework/app_lifecycle.go
- framework/app_cache.go
- framework/app_log.go
- framework/app_security.go
- framework/constants.go
- framework/lang.go 或当前语言加载实现文件
- framework/view.go 或当前视图初始化实现文件
- framework/app_paths.go（新增）
- framework/app_paths_test.go（新增）
- framework/app_config_test.go（新增）

### 3.1 先写失败测试

- [ ] 用 t.TempDir() 创建项目根目录、app/index、app/admin、根 config 和两个应用的 config、lang、view 文件，断言每个 App 的应用路径、运行时目录、配置覆盖和语言文件互不串用。
- [ ] 断言共享资源仍从项目根目录解析：public、异常模板和宿主级配置不能随当前应用切换到另一个应用目录。
- [ ] 断言应用名含 ..、路径分隔符、控制字符或经过清理后为空时构造失败；应用路径不在项目根目录内时构造失败。
- [ ] 断言同一项目中两个应用的日志、缓存和会话文件落在不同的应用运行时命名空间，不会因同名文件互相覆盖。

### 3.2 实现路径和配置加载

- [ ] 给 App 增加以下字段，并确保构造后不可被请求处理流程修改：

~~~go
ApplicationName string
ApplicationPath string
RuntimePath     string
~~~

- [ ] 在 framework/app_paths.go 集中实现 ApplicationConfigPath、ApplicationLangPath、ApplicationViewPath、ApplicationRuntimePath、ProjectPublicPath 和 ExceptionTemplatePath，所有调用点改用这些方法，禁止散落 filepath.Join 形成新的路径魔数。
- [ ] 保持 BasePath 表示项目根目录；默认单应用把应用名设为 index，应用路径为 BasePath/app/index。不把 BasePath 重定义成应用目录，避免破坏宿主资源和 CLI 路径语义。
- [ ] 初始化顺序调整为：加载根 .env 和根配置；读取应用定义和应用配置覆盖；应用级语言、视图、日志、缓存、会话、数据库等资源使用当前应用边界；宿主监听和 TLS 配置只从根配置读取。
- [ ] 将当前 BasePath + \"/app/lang\"、视图根、日志、缓存、异常模板和安全存储路径分别迁移到路径辅助方法；公共静态文件仍从项目根 public 提供。
- [ ] 使用 `BuildApp`、`BuildConsoleApp` 和显式未初始化入口统一校验构造失败；多应用管理器使用内部构造入口创建带名称和路径的 App，不再提供旧构造函数兼容别名。
- [ ] 检查 framework/app.go 中所有初始化阶段的 Config、Lang、View、Cache、Log、Session 和数据库运行时路径，逐项改为当前应用路径或明确的宿主路径。

### 3.3 验证和提交

- [ ] 运行 gofmt 处理本阶段 Go 文件。
- [ ] 运行 go test ./framework -run 'Test(AppPath|ApplicationConfig|ApplicationResource)' -count=1。
- [ ] 运行 go test ./framework/... -count=1，确认现有单应用测试没有因 BasePath 语义变化回归。
- [ ] 暂存本阶段文件并提交 refactor: add application resource boundaries。

## 4. 增加 ApplicationManager 和多应用生命周期

**Files:**

- framework/application_manager.go（新增）
- framework/application_manager_test.go（新增）
- framework/app_lifecycle.go
- framework/app_lifecycle_test.go
- framework/app.go

### 4.1 先写失败测试

- [ ] 测试管理器根据多个 ApplicationDefinition 创建多个独立 App，每个定义的 Register 回调恰好执行一次。
- [ ] 测试所有应用先完成初始化，再按定义顺序启动服务提供者；任一应用初始化或 provider 注册/启动失败时，管理器返回错误且不调用宿主 Kernel。
- [ ] 测试关闭顺序为应用定义顺序的逆序；关闭错误会被返回，同时仍继续关闭其他应用。
- [ ] 测试重复调用 Boot、Run 和 Close 的幂等/错误语义与现有 App 生命周期一致；单应用 App.Run 现有行为继续通过。
- [ ] 测试初始化错误包含应用名和阶段名，例如 admin: boot provider Foo: ...，避免多应用错误只显示无上下文的底层错误。

### 4.2 实现管理器

- [ ] 新增 ApplicationManager，提供以下稳定入口：

~~~go
func NewApplicationManager(basePath string) (*ApplicationManager, error)
func (manager *ApplicationManager) Applications() map[string]*App
func (manager *ApplicationManager) Application(name string) (*App, bool)
func (manager *ApplicationManager) DefaultApplication() *App
func (manager *ApplicationManager) Boot() error
func (manager *ApplicationManager) Run(kernel Kernel) error
func (manager *ApplicationManager) Close() error
~~~

- [ ] 管理器启动时读取注册定义快照，先校验默认应用、名称、路径和重复域名/路径配置，再按稳定顺序创建所有 App；不能因为 map 遍历顺序不同而改变启动行为。
- [ ] 管理器 Boot 只负责所有应用初始化和 provider 启动，Run 负责调用一次宿主 Kernel，Close 负责逆序关闭；禁止每个子 App 单独启动 HTTP 监听器。
- [ ] 保留现有 App.Run 的单应用生命周期；把共用的启动错误检查、provider 启动和关闭逻辑抽成内部方法，避免两套逻辑漂移。
- [ ] 对启动失败执行已初始化应用的逆序回滚；对运行期关闭执行所有应用的逆序关闭，并使用现有错误组合方式保留多个错误。
- [ ] 管理器只拥有应用实例，不把当前请求应用写入管理器字段；请求处理时使用应用名查表，保证并发请求之间没有全局当前应用。

### 4.3 验证和提交

- [ ] 运行 gofmt。
- [ ] 运行 go test ./framework -run 'Test(ApplicationManager|AppLifecycle)' -count=1。
- [ ] 运行 go test -race ./framework -run 'Test(ApplicationManager|ApplicationRegistry)' -count=1；若当前 Windows 工具链不支持 race，记录工具链错误并继续常规测试。
- [ ] 提交 feat: add multi-application lifecycle manager。

## 5. 实现 ThinkPHP 风格的应用解析器

**Files:**

- framework/application_resolver.go（新增）
- framework/application_resolver_test.go（新增）
- framework/config.go 或当前配置访问实现文件
- framework/application_definition.go

### 5.1 先写失败测试

- [ ] 使用表驱动测试覆盖：精确域名绑定、子域名绑定、通配域名、端口剥离、域名大小写归一化、路径首段匹配、路径前缀剥离、域名绑定不剥离路径、默认应用回退、拒绝应用、非法应用名、空路径和双重编码路径。
- [ ] 覆盖优先级：域名绑定优先于 URL 首段，显式 app_map 优先于同名目录推断，拒绝列表优先于默认回退，未匹配请求只在 app_express=true 时落到默认应用。
- [ ] 覆盖 /admin、/admin/、/admin/users 的边界，确保不会把 /administrator 误判为 admin；路径重写后保留查询参数。
- [ ] 覆盖请求 Host 含非法字符、应用名含路径分隔符或 URL 编码绕过时返回可判定的 404/400 错误，不允许访问项目根目录之外的文件。

### 5.2 实现解析协议

- [ ] 新增 ApplicationResolution，至少包含 Name、OriginalPath、RewrittenPath、PathPrefix 和 DomainBound；原始路径只读保存，改写路径只用于当前请求副本。
- [ ] 新增 ApplicationResolver，从根配置读取并校验 default_app、app_map、domain_bind、deny_app_list 和 app_express；配置错误在管理器启动阶段失败，不延迟到首个请求。
- [ ] 实现域名绑定匹配规则：精确 host、子域名、通配符和可选端口规范化；域名绑定成功后应用内路径保持原样。
- [ ] 实现路径应用匹配：只检查 URL path 的第一个合法段；匹配后移除该段并保证结果至少为 /；不对请求对象原地改写。
- [ ] 实现拒绝和回退策略：无法解析、应用不存在、应用被拒绝或 app_express=false 未命中时返回明确的未找到错误；只有 app_express=true 才允许默认应用接管。
- [ ] 将解析器依赖的应用定义和 App 路径信息设为只读快照；解析过程不得触发配置加载、注册回调或文件系统扫描。

### 5.3 验证和提交

- [ ] 运行 gofmt。
- [ ] 运行 go test ./framework -run 'Test(ApplicationResolver|ApplicationResolution)' -count=1。
- [ ] 提交 feat: resolve applications by host and path。

## 6. 增加请求级应用上下文和应用感知 URL

**Files:**

- framework/context/request.go
- framework/context/application.go（新增）
- framework/context/request_application_test.go（新增）
- framework/app.go
- framework/route/route_url.go
- framework/app_url.go（新增）
- framework/app_url_test.go（新增）

### 6.1 先写失败测试

- [ ] 测试同一个 App 在并发请求中处理不同应用上下文时，URL 生成结果分别带有各自路径前缀，不能依赖或修改 App 的当前应用字段。
- [ ] 测试域名绑定应用生成 URL 时不重复追加应用名；路径应用生成站内 URL 时追加当前应用前缀；显式指定目标应用时根据 app_map 和域名绑定返回目标应用 URL。
- [ ] 测试路由名不存在、目标应用不存在、非法路径和空请求上下文时返回错误，不生成静默错误 URL。
- [ ] 保持现有 Router.URL 的应用内相对路径测试；新增的应用感知能力通过 App/Request 层提供，不改变路由器的纯函数职责。

### 6.2 实现请求上下文

- [ ] 在 framework/context/application.go 增加不可变 ApplicationContext：应用名、原始路径、改写前缀、域名绑定标志和原始 Host；在 Request 上提供设置/读取方法，读取返回值拷贝。
- [ ] 在 App 上增加应用感知 URL 方法，明确区分“应用内 URL”和“宿主 URL”；所有路径拼接使用 url.URL 或现有安全拼接函数，不能手工拼接未转义查询参数。
- [ ] 让 RouteURL 先调用当前 App 的路由器生成应用内路径，再根据请求的 ApplicationContext 处理应用前缀或域名映射；不写入共享路由状态。
- [ ] 检查现有 Domain、URL、AssetURL 调用点，保持单应用结果不变，并让多应用请求生成当前请求可访问的 URL。

### 6.3 验证和提交

- [ ] 运行 gofmt。
- [ ] 运行 go test ./framework/context ./framework/route ./framework -run 'Test(RequestApplication|AppURL|RouteURL)' -count=1。
- [ ] 提交 feat: add request application context and url scope。

## 7. 建立统一 MultiHttp 宿主和请求分发

**Files:**

- framework/http/multi_app.go（新增）
- framework/http/multi_app_test.go（新增）
- framework/http/http.go
- framework/http/server.go
- framework/http/static.go
- framework/http/config.go 或当前 HTTP 配置实现文件
- framework/http/http_test.go
- framework/http/server_test.go

### 7.1 先写失败测试

- [ ] 用 httptest.NewRecorder 和两个内存应用定义测试：/index/users、/admin/users、域名绑定 /users 分别到达正确应用，响应体、应用上下文和应用内路由互不串用。
- [ ] 测试未知应用、拒绝应用、非法路径和应用初始化失败均返回正确错误；未知应用请求不能触发任何应用控制器或 provider。
- [ ] 测试原始请求对象在分发后保持不变；应用收到的是克隆请求，查询参数、Header、Body、TLS 信息和 RemoteAddr 保持一致，只有 URL path 按解析结果改写。
- [ ] 测试静态文件、压缩、异常模板和 HttpRun/HttpEnd 事件仍按子应用处理，但监听器、访问日志和 TLS 配置只初始化一次。
- [ ] 测试 MultiHttp.Run 只调用一次底层监听/服务器启动入口；不得为每个应用调用 Http.Run。
- [ ] 测试并发请求同时访问两个应用，应用 A 的配置、语言、路由和响应不能被应用 B 覆盖。

### 7.2 实现统一宿主

- [ ] 新增以下宿主 API：

~~~go
type MultiHttp struct {
\tmanager  *framework.ApplicationManager
\thandlers map[string]*Http
}

func NewMultiHttp(manager *framework.ApplicationManager) (*MultiHttp, error)
func (host *MultiHttp) ServeHTTP(writer http.ResponseWriter, request *http.Request)
func (host *MultiHttp) Run() error
~~~

- [ ] NewMultiHttp 在监听前为每个 App 创建一个 Http 子处理器，所有子处理器共享已经校验过的宿主监听配置，但不共享 App 运行时组件。
- [ ] ServeHTTP 按以下顺序执行：解析应用；未知/拒绝时返回错误；克隆请求和 URL；改写应用路径；写入请求级应用上下文；调用对应 Http.ServeHTTP。整个过程不得改变原请求或管理器的当前状态。
- [ ] 将当前 Http.Run 的监听实现抽成可复用的宿主服务器构造；MultiHttp.Run 使用一次 TCP/TLS/HTTP3 服务器，保留现有 graceful shutdown、信号处理、压缩和 HTTP/3 配置行为。
- [ ] 将静态文件根目录改用项目根 public；异常模板改用项目根 framework/exception/tpl；视图和语言仍由子 App 的路径辅助方法提供。
- [ ] 为子处理器设置应用名和请求上下文，而不是临时修改宿主子处理器持有的路径、路由、配置或任何共享字段。
- [ ] 将宿主启动错误、子应用构造错误、请求解析错误分别包装成包含应用名/阶段的错误；监听开始前任何子应用错误都必须返回。

### 7.3 验证和提交

- [ ] 运行 gofmt。
- [ ] 运行 go test ./framework/http -run 'Test(MultiHttp|HttpServer|Static|HTTP)' -count=1。
- [ ] 运行 go test -race ./framework/http ./framework/route；若 race 工具链受 Windows 环境限制，记录失败原因并完成普通并发测试。
- [ ] 提交 feat: add unified multi-application http host。

## 8. 迁移示例应用到 app/index 并接入显式注册

**Files:**

- app/index/application.go（新增）
- app/index/controller/user.go（由旧文件迁移内容后单独创建）
- app/index/middleware/request_id.go（由旧文件迁移内容后单独创建）
- app/index/model/user.go（由旧文件迁移内容后单独创建）
- app/index/validate/user_file_validate.go（由旧文件迁移内容后单独创建）
- app/index/lang/*（按现有语言文件逐个创建）
- app/index/route/app.go（由旧文件迁移内容后单独创建）
- 现有 app/*、route/* 中对应旧文件（逐个删除）
- main.go
- cmd/think/main.go
- 相关现有应用测试文件

### 8.1 先写失败测试

- [ ] 增加应用注册集成测试，断言 index 应用只通过 app/index/application.go 注册控制器、路由加载器、中间件、provider 和事件监听器。
- [ ] 增加目录约束测试或静态检查：仓库中不再存在旧的根级应用目录和根级 route 包引用；所有 thinkgo/app/... 导入都指向 thinkgo/app/index/... 或明确的其他应用。
- [ ] 增加启动测试，断言根入口和 CLI 的 run 都只构造一个宿主，且 index 应用能够完整加载自己的 route/lang/view 配置。

### 8.2 逐文件迁移和注册

- [ ] 使用 apply_patch 逐个创建 app/index 目录下的源文件，保留原有业务行为和测试断言；只修改包声明、相对路径和注册入口，不顺手重写业务逻辑。
- [ ] 删除旧根级 app/controller、app/middleware、app/model、app/validate、app/lang 和 route 文件，确保没有兼容目录、转发包或重复注册。
- [ ] 创建 app/index/application.go，导出明确的应用注册函数，按照“控制器、路由、中间件、事件、服务提供者”的顺序把 app/index 内部组件注册到传入的 *framework.App；注册失败原样返回并带应用上下文。
- [ ] 删除示例组件中的包级 init 注册；应用包的注册回调成为唯一生产注册入口。
- [ ] 修改 main.go 和 cmd/think/main.go 的空白导入，改为应用定义包；根入口使用 ApplicationManager + MultiHttp，不再为单一 app 直接创建 HTTP 监听器。
- [ ] 保持单应用开发体验：当只注册 index 时，现有访问路径、模板、数据库和 CLI 结果与迁移前一致；多应用路径行为由第 5、7 阶段负责。

### 8.3 验证和提交

- [ ] 运行 gofmt 处理迁移后的 Go 文件。
- [ ] 运行 rg -n 'thinkgo/app/(controller|middleware|model|validate|lang)|thinkgo/route|app/(controller|middleware|model|validate|lang)|^route/' --glob '*.go'，结果只允许来自明确的迁移测试或不存在的旧路径说明；实现代码不能有旧路径。
- [ ] 运行 go test ./app/... ./framework/... ./cmd/... -count=1。
- [ ] 提交 refactor: move index application components under app index。

## 9. 更新 CLI、代码生成和应用级开发工作流

**Files:**

- cmd/think/main.go
- framework/console/command/run.go
- framework/console/command/run_test.go
- framework/console/command/application_option.go（新增）
- framework/console/command/application_option_test.go（新增）
- framework/console/command/make_helpers.go
- framework/console/command/application_source.go（新增）
- framework/console/command/application_source_test.go（新增）
- framework/console/command/make_controller.go
- framework/console/command/make_model.go
- framework/console/command/make_middleware.go
- framework/console/command/make_validate.go
- framework/console/command/make_event.go
- framework/console/command/make_service.go
- framework/console/command/make_listener.go
- framework/console/command/make_subscribe.go
- framework/console/command/make_command.go
- 各 make_*_test.go
- framework/console/command/route_list.go、config_dump.go、clear.go 及其测试（如实际读取应用路径）

### 9.1 先写失败测试

- [ ] 测试 --app admin 解析成功，未传时使用配置的默认应用；应用名非法、应用不存在和路径越界时返回错误。
- [ ] 测试 think run 创建一个 ApplicationManager 和一个 MultiHttp，不会对每个 App 调用旧的 Http.Run；保留 --port、TLS、HTTP/3 和 air 参数行为。
- [ ] 测试 make controller/model/middleware/validate/event/service/listener/subscribe/command 的输出路径均为 app/<app>/...，默认应用为 index，显式 --app 能切换到 admin。
- [ ] 测试生成的源码包名、导入路径和注册调用只引用目标应用；生成器不能写到另一个应用或根级旧目录。
- [ ] 测试应用注册源文件更新器：首次生成创建合法的 application.go 注册段；重复运行幂等；同名组件不重复注册；插入失败时不破坏原文件。
- [ ] 测试 route:list、配置查看、缓存清理等命令在指定应用下读取 ApplicationPath 和 RuntimePath，没有把宿主根路径误当应用路径。

### 9.2 实现应用选择和生成器迁移

- [ ] 新增统一的 --app 参数解析器和应用名校验，所有需要应用上下文的命令复用同一实现；不得在各个 make_* 文件中重复解析字符串。
- [ ] 为命令运行时增加“宿主运行”和“单应用控制台”两条明确路径：run 使用 manager/host；迁移、缓存、路由查看等控制台命令通过 --app 选择一个已构造的 App。
- [ ] 调整 runOperations 的依赖注入，使测试可以分别替换 manager 工厂、host 工厂和运行函数；不在命令测试中启动真实网络监听。
- [ ] 修改 make_helpers.go，增加 ApplicationPath、应用相对目标安全校验和应用注册源文件路径；继续使用现有 os.Root/独占创建策略，拒绝 ..、绝对路径和符号链接逃逸。
- [ ] 将所有生成器的目标目录从根级 app/<type> 改为 app/<app>/<type>；包括用户明确要求的 controller、middleware、model、validate，以及同一应用边界内的 event、service、listener、subscribe、command。
- [ ] 更新生成模板，移除依赖包级 init 的全局注册；生成器必须同时更新目标应用的显式注册源文件，使新组件生成后可以被该应用加载。
- [ ] 在 application_source.go 中使用 Go 解析/格式化能力或受控标记区更新注册文件：导入别名按包路径稳定排序，注册语句按组件名稳定排序，重复项幂等，任何解析或写入失败都保留原文件并返回错误；禁止用无边界的字符串替换破坏用户代码。
- [ ] 为每类生成器补充中文注释和应用参数说明；模板必须包含完整的注册逻辑，不出现空函数或旧路径。

### 9.3 验证和提交

- [ ] 运行 gofmt。
- [ ] 运行 go test ./framework/console/command -run 'Test(Command|Make|Application)' -count=1。
- [ ] 运行命令包全量测试 go test ./framework/console/command -count=1。
- [ ] 提交 feat: scope cli and generators by application。

## 10. 完成架构文档和引用迁移

**Files:**

- docs/架构/架构总览.md
- docs/架构/请求流程.md
- docs/架构/入口文件.md
- docs/架构/多模块和多应用.md
- docs/架构/URL访问.md
- docs/架构/容器和依赖注入.md
- docs/架构/服务.md
- docs/架构/中间件.md
- docs/架构/事件.md
- docs/基础/目录结构.md
- docs/命令行/启动服务.md
- docs/命令行/代码生成命令.md
- docs/README.md
- 其他由链接检查发现的 ThinkGo 文档引用文件

- [ ] 更新架构总览的目录树，明确 app/<应用名>/ 下按 ThinkPHP 风格组织组件，标出宿主级和应用级资源边界。
- [ ] 更新请求流程，准确描述“单监听器 -> 应用解析 -> 请求克隆/路径改写 -> 子 App 中间件/路由/控制器 -> 统一响应”的顺序，以及域名应用不剥离路径的规则。
- [ ] 更新入口文件和启动服务文档，使用 ApplicationManager + MultiHttp；说明初始化失败发生在监听前，关闭时按应用逆序执行。
- [ ] 重写多模块和多应用文档中关于“未内置多应用”的旧结论，加入 default_app、app_map、domain_bind、deny_app_list、app_express 的配置语义、优先级和安全约束。
- [ ] 更新 URL、容器、服务、中间件、事件文档，说明应用实例隔离、应用级注册、请求级 URL 前缀和单应用兼容边界。
- [ ] 更新目录结构和 CLI/代码生成文档，给出 --app 示例和生成目标路径；不保留旧根级路径链接。
- [ ] 使用 rg -n 'app/controller|app/middleware|app/model|app/validate|app/lang|根级.*route|未内置多应用|不支持多应用' docs README.md 检查过时描述，逐条处理，不能用批量替换。
- [ ] 检查所有相对链接目标是否存在；架构目录链接使用当前中文文件名，删除或移动旧文档后的引用全部更新。

### 验证和提交

- [ ] 运行文档链接检查命令或仓库现有文档校验入口；若没有专用入口，使用只读 rg 检查 Markdown 链接目标和旧路径引用。
- [ ] 运行 git diff --check，若命中本次未修改的既有 README 空白问题，单独记录，不擅自修改无关文件。
- [ ] 提交 docs: document thinkgo multi-application architecture。

## 11. 全量验证、并发验证和交付检查

**Files:** 无新增临时工具；只运行验证命令并检查最终差异。

- [ ] 运行 gofmt -l，输出为空；只对本次修改的 Go 文件运行 gofmt -w 修复格式。
- [ ] 运行 go test ./... -count=1，记录全部包结果。
- [ ] 运行 go test -race ./framework/http ./framework/route ./framework/context ./framework/console/command -count=1；若 Windows race 工具链不可用，保留错误证据，并运行可用的普通并发测试。
- [ ] 运行 go vet ./...，处理本次引入的诊断；既有无关诊断单独列出。
- [ ] 运行覆盖率命令 go test ./framework/... ./app/... -coverprofile=coverage.out，确认核心新增包达到目标；使用完毕删除临时 coverage.out，不把临时工具或产物提交。
- [ ] 用 rg 检查生产代码不存在旧平面目录导入、全局当前应用变量、每应用独立 ListenAndServe 调用和未处理的注册错误。
- [ ] 用 git diff --stat、git diff --name-status 和 git diff --check 检查只包含本次架构升级和用户已确认的文档更新；确认没有临时文件、构建产物或意外删除。
- [ ] 运行最终 git status --short，确认所有本次文件已提交、用户无关改动仍保持原状；在最终响应中只声明实际通过的验证项。

## 12. 最终验收标准

- [ ] app/index 能作为默认应用完整启动；旧的根级 app/controller、app/middleware、app/model、app/validate、app/lang 和 route 不再参与编译或运行。
- [ ] 至少两个应用能在同一进程、同一监听器下并发处理请求，按域名和路径正确解析，且配置、路由、语言、注册表和运行时目录互不污染。
- [ ] 所有应用在监听前完成初始化和 provider 启动；任一应用失败时不会出现半启动宿主；关闭顺序可验证且错误不吞失。
- [ ] think run --app <name>、各类 make:* --app <name>、路由/配置/缓存命令的应用边界一致，生成文件位于目标应用目录并能被显式注册入口加载。
- [ ] 架构、入口、请求流程、目录结构、URL、CLI 文档与真实代码一致，所有旧路径和旧结论引用已更新。
- [ ] 全量测试、关键包 race 测试、vet、格式化和覆盖率结果已记录；未把基线失败或环境限制误报为本次成功。
