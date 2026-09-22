package framework

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/filesystem"
	"github.com/zhuhanxin0308/thinkgo/v3/health"
	"github.com/zhuhanxin0308/thinkgo/v3/lang"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/migration"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
	"github.com/zhuhanxin0308/thinkgo/v3/view"
)

// defaultDatabaseConnectionName 是未配置数据库时空管理器使用的稳定内部默认名称。
const defaultDatabaseConnectionName = "default"

// App 应用实例
// 对应 ThinkPHP 8 的 think\App
type App struct {
	container                  *Container
	BasePath                   string
	AppName                    string
	ApplicationPath            string
	RuntimePath                string
	DebugMode                  bool
	metadataMu                 sync.RWMutex
	debugOverrideSet           bool
	debugOverride              bool
	applicationName            string
	applicationDefinition      ApplicationDefinition
	applicationCatalog         *applicationCatalog
	nativeApplicationPath      string
	nativeRuntimeBasePath      string
	projectConfiguration       *projectConfigurationState
	projectConfigSnapshot      *config.Config
	namespace                  string
	baseEnvName                string
	envName                    string
	configExt                  string
	consoleMode                bool
	beginTime                  atomic.Uint64
	beginMem                   atomic.Uint64
	route                      *route.Router
	routeFacade                *Route
	middleware                 *middleware.Pipeline
	applicationMiddleware      *middleware.Pipeline
	db                         *db.DB
	dbManager                  *db.Manager
	config                     *config.Config
	env                        *env.Env
	log                        *log.Log
	metrics                    *metrics.Registry
	health                     *health.Registry
	view                       *view.View
	cache                      *cache.Cache
	filesystem                 *filesystem.Filesystem
	event                      *event.Dispatcher
	lang                       *lang.Lang
	cookie                     *cookie.Cookie
	session                    *session.Session
	debug                      *debug.Debug
	migrations                 *migration.Registry
	telemetry                  *telemetry.Tracing
	Kernel                     Kernel
	registry                   applicationRegistry
	registrationMu             sync.RWMutex
	registrationClosed         bool
	loadingNativeDefinition    bool
	appInitOnce                sync.Once
	appInitErr                 error
	routeLoadOnce              sync.Once
	routeLoadErr               error
	startupMu                  sync.RWMutex
	startupErr                 error
	startupErrorCount          int
	startupErrorOmitted        int
	locationMu                 sync.RWMutex
	location                   *time.Location
	securityWarningBits        uint32
	serviceMutationMu          sync.Mutex
	resources                  serviceResources
	applicationServiceMu       sync.RWMutex
	applicationServices        []interface{}
	providers                  providerLifecycle
	lifecycle                  appLifecycle
	sessionEnabled             bool
	csrfEnabled                bool
	traceEnabled               bool
	metricsEnabled             bool
	operationalRoutesEnabled   bool
	securityProfile            SecurityProfile
	databaseStartupPolicy      DatabaseStartupPolicy
	operationalAccess          OperationalAccess
	operationalAllowedPrefixes []netip.Prefix
	databaseReadinessMu        sync.RWMutex
	databaseReadinessErr       error
	// skipDatabaseInit 为 true 时，Initialize 跳过数据库连接。
	// 供不需要数据库的控制台命令（version/list/make:* 等）使用，
	// 避免每次执行命令都尝试连库并打印连接失败日志。
	skipDatabaseInit bool
}

// NewApp 创建尚未初始化的应用实例。
// 对应 ThinkPHP 的 new App()：配置、服务启动和路由加载由 Http 首次运行触发。
func NewApp(basePath ...string) *App {
	return NewAppUninitialized(basePath...)
}

// NewAppUninitialized 创建尚未初始化的项目应用实例。
//
// 该入口只构造基础上下文和容器，不加载配置、不创建缓存、Session 或数据库连接，
// 调用方可以在注册应用组件后显式调用 Initialize。
func NewAppUninitialized(basePath ...string) *App {
	return newAppWithOptions(false, basePath...)
}

// BuildApp 创建并初始化项目应用实例，初始化失败时不会返回半初始化对象。
func BuildApp(basePath ...string) (*App, error) {
	return buildApp(false, basePath...)
}

// NewConsoleAppUninitialized 创建尚未初始化且跳过数据库连接的控制台应用实例。
func NewConsoleAppUninitialized(basePath ...string) *App {
	return newAppWithOptions(true, basePath...)
}

// BuildConsoleApp 创建并初始化控制台应用，初始化失败时不会返回半初始化对象。
func BuildConsoleApp(basePath ...string) (*App, error) {
	return buildApp(true, basePath...)
}

// buildApp 统一处理显式构建入口，确保初始化失败时释放已创建的资源。
func buildApp(skipDatabase bool, basePath ...string) (*App, error) {
	app := newAppWithOptions(skipDatabase, basePath...)
	if err := app.Initialize(); err != nil {
		return nil, errors.Join(err, app.Close())
	}
	return app, nil
}

func newAppWithOptions(skipDatabase bool, basePath ...string) *App {
	var path string
	constructionErrors := make([]error, 0, 2)
	if len(basePath) > 1 {
		constructionErrors = append(constructionErrors, fmt.Errorf("应用根目录最多只能指定一次"))
	}
	if len(basePath) > 0 {
		path = strings.TrimSpace(basePath[0])
		if path == "" {
			constructionErrors = append(constructionErrors, fmt.Errorf("应用根目录不能为空"))
			path = "."
		}
	} else {
		var err error
		path, err = os.Getwd()
		if err != nil {
			constructionErrors = append(constructionErrors, fmt.Errorf("读取当前工作目录失败: %w", err))
			path = "."
		}
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		constructionErrors = append(constructionErrors, fmt.Errorf("解析应用根目录失败: %w", err))
	} else {
		path = filepath.Clean(absolutePath)
	}
	if info, statErr := os.Stat(path); statErr != nil {
		constructionErrors = append(constructionErrors, fmt.Errorf("访问应用根目录失败: %w", statErr))
	} else if !info.IsDir() {
		constructionErrors = append(constructionErrors, fmt.Errorf("应用根目录不是目录: %s", path))
	}

	applicationPath := filepath.Join(path, "app")
	runtimePath := filepath.Join(path, "runtime")

	router := route.NewRouter()
	app := &App{
		container:             NewContainer(),
		BasePath:              path,
		AppName:               defaultApplicationDisplayName,
		ApplicationPath:       applicationPath,
		RuntimePath:           runtimePath,
		nativeRuntimeBasePath: runtimePath,
		namespace:             "app",
		configExt:             ".json",
		consoleMode:           skipDatabase,
		route:                 router,
		middleware:            middleware.NewPipeline(),
		applicationMiddleware: middleware.NewPipeline(),
		config:                config.NewConfig(),
		env:                   env.NewEnv(),
		// 构造阶段只保留无驱动日志占位器，真实日志驱动由 Log Provider 按配置创建。
		log:              log.NewLog(),
		metrics:          metrics.NewRegistry(),
		health:           health.NewRegistry(),
		event:            event.NewDispatcher(),
		lang:             lang.NewLang(),
		debug:            debug.NewDebug(),
		migrations:       migration.NewRegistry(),
		telemetry:        telemetry.Disabled(),
		location:         defaultApplicationLocation(),
		lifecycle:        appLifecycle{requiresInitialization: true, state: ApplicationStateConstructed},
		skipDatabaseInit: skipDatabase,
	}
	app.routeFacade = newRouteFacade(app, router)
	app.projectConfiguration = newProjectConfigurationState(app)
	if err := app.bindFoundationServices(); err != nil {
		app.recordStartupError(fmt.Errorf("装配基础服务失败: %w", err))
	}
	// 内置 Provider 在构造阶段只完成注册，依赖配置的资源由 Initialize 阶段按固定顺序装配。
	for _, provider := range []ServiceProvider{
		&appLogProvider{},
		&appLangProvider{},
		&appCacheProvider{},
		&appFilesystemProvider{},
		&appCookieProvider{},
		&appSessionProvider{},
		&appDatabaseProvider{},
		&appViewProvider{},
	} {
		if err := app.RegisterProvider(provider); err != nil {
			app.recordStartupError(fmt.Errorf("注册内置 Provider %T 失败: %w", provider, err))
		}
	}
	for _, constructionErr := range constructionErrors {
		app.recordStartupError(constructionErr)
	}
	return app
}

// StartupError 返回初始化阶段记录的启动错误（无错误时为 nil）。
// 供绕过 App.Run 直接驱动内核的调用方（如控制台 run 命令）在启动前自检。
func (app *App) StartupError() error {
	if app == nil {
		return ErrNilApplication
	}
	app.startupMu.RLock()
	defer app.startupMu.RUnlock()
	if app.startupErrorOmitted > 0 {
		return fmt.Errorf("%w\n另有省略 %d 条启动错误", app.startupErr, app.startupErrorOmitted)
	}
	return app.startupErr
}

// Kernel 定义应用运行内核。
type Kernel interface {
	Run() error
}

// IsDebug 返回是否启用调试模式。
func (app *App) IsDebug() bool {
	if app == nil {
		return false
	}
	app.metadataMu.RLock()
	enabled := app.DebugMode
	app.metadataMu.RUnlock()
	return enabled
}

// Environment 返回当前运行环境；未显式设置时与 ThinkPHP 一致为空。
func (app *App) Environment() string {
	if app == nil {
		return ""
	}
	environmentName := ""
	if app.config != nil {
		environmentName = strings.TrimSpace(app.config.GetString("app.app_env"))
	}
	if environmentName == "" {
		_, configuredName := app.environmentNames()
		environmentName = strings.TrimSpace(configuredName)
	}
	if environmentName == "" && app.env != nil {
		environmentName = strings.TrimSpace(app.env.Get("env_name", ""))
	}
	return environmentName
}

// ==================== URL生成方法 ====================

// Domain 获取服务器域名
func (app *App) Domain() string {
	if app == nil || app.config == nil {
		return "https://localhost"
	}
	domain := strings.TrimSpace(app.config.GetString("app.server.domain", "https://localhost"))
	if domain == "" || strings.ContainsAny(domain, "\r\n\t") {
		return "https://localhost"
	}
	return domain
}

// URL 生成完整的URL
// 如果path已经是完整URL（以http://或https://开头），则直接返回
// 否则拼接服务器域名和路径
func (app *App) URL(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\r\n\t") {
		return ""
	}
	// 完整 URL 只允许带 host 的 HTTP(S)，避免畸形地址和协议绕过。
	lowerPath := strings.ToLower(path)
	if strings.HasPrefix(lowerPath, "http://") || strings.HasPrefix(lowerPath, "https://") {
		parsed, err := url.ParseRequestURI(path)
		if err != nil || parsed.Host == "" {
			return ""
		}
		return path
	}
	domain := strings.TrimRight(app.Domain(), "/")
	return domain + "/" + strings.TrimLeft(path, "/")
}

// AssetURL 生成静态资源URL
func (app *App) AssetURL(path string) string {
	return app.URL(path)
}

// initialize 执行一次应用组件装配，由 Initialize 的 sync.Once 保护。
func (app *App) initialize() {
	app.captureInitializationStart()
	if err := app.validateInitializationFoundation(); err != nil {
		app.recordStartupError(err)
		return
	}

	// 1. 项目环境与配置只解析一次；每个业务应用取得彼此隔离的工作副本。
	if err := app.prepareProjectConfiguration(); err != nil {
		app.recordStartupError(err)
	}
	app.applyApplicationRuntimeConfig()
	app.validateConsoleApplicationConfig()

	// 4. 按 ThinkPHP App.load 顺序装配根 app/event、app/service。
	// 原生多应用的 app/<name> 业务定义在全局 AppInit 后叠加，保持根应用与
	// 具体业务应用的生命周期边界。
	if err := app.loadApplicationDefinition(); err != nil {
		app.recordStartupError(fmt.Errorf("加载应用定义失败: %w", err))
		return
	}

	app.LoadLangPack()
	if err := app.dispatchAppInit(); err != nil {
		app.recordStartupError(err)
		return
	}
	if app.hasNativeApplication() {
		if err := app.captureProjectApplicationConfig(); err != nil {
			app.recordStartupError(err)
			return
		}
		if err := app.loadNativeApplication(); err != nil {
			app.recordStartupError(err)
			return
		}
		// 应用配置在全局配置之后覆盖，并在 Provider 初始化前形成最终快照。
		app.applyApplicationRuntimeConfig()
		app.validateConsoleApplicationConfig()
		app.LoadLangPack()
	}

	// 5. Init Providers：配置与应用服务注册已完成，按确定顺序装配配置型 Provider。
	if err := app.initializeProviders(); err != nil {
		app.recordStartupError(err)
	}

	// 应用注册回调产生的控制器、中间件和路由在此统一装配。
	app.initializeApplicationAssembly()
}

func (app *App) validateInitializationFoundation() error {
	missing := make([]string, 0, 7)
	if app.container == nil {
		missing = append(missing, "container")
	}
	if app.env == nil {
		missing = append(missing, "env")
	}
	if app.config == nil {
		missing = append(missing, "config")
	}
	if app.event == nil {
		missing = append(missing, "event")
	}
	if app.route == nil {
		missing = append(missing, "route")
	}
	if app.middleware == nil {
		missing = append(missing, "middleware")
	}
	if app.lang == nil {
		missing = append(missing, "lang")
	}
	if len(missing) > 0 {
		return fmt.Errorf("应用基础服务未完成构造: %s", strings.Join(missing, ", "))
	}
	return nil
}

const maxDatabaseInitializationConcurrency = 4

type databaseConnectionSpec struct {
	name   string
	config db.Config
}

type databaseConnectionResult struct {
	name     string
	database *db.DB
	err      error
}

// initDatabaseConnections 按配置并行建立数据库连接，并把默认连接绑定到 ServiceDB。
// 并发数固定有界，结果在安装前按连接名称排序，避免启动顺序和默认连接语义漂移。
func (app *App) initDatabaseConnections(dbConfigData map[string]interface{}, defaultConn string) {
	specs := app.databaseConnectionSpecs(dbConfigData, defaultConn)

	results := initializeDatabaseConnections(specs)
	for _, spec := range specs {
		result := results[spec.name]
		if result.err != nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(result.err)
				if app.databaseStartupPolicy == DatabaseStartupRequired {
					app.recordStartupError(fmt.Errorf("required default database connection %q failed: %w", spec.name, result.err))
				}
			}
			if app.log != nil {
				message := fmt.Sprintf("数据库连接 %q 失败（应用仍可启动，但 readiness 将失败，启动策略=%s）: %s", spec.name, app.databaseStartupPolicy, redactDatabaseConnectionError(result.err))
				if app.databaseStartupPolicy == DatabaseStartupRequired && spec.name == defaultConn {
					message = fmt.Sprintf("数据库连接 %q 失败（required 策略将阻断启动）: %s", spec.name, redactDatabaseConnectionError(result.err))
				}
				app.log.Warning(message)
			}
			if app.databaseStartupPolicy != DatabaseStartupRequired || spec.name != defaultConn {
				if retryErr := app.registerDatabaseConnectionFactory(spec, defaultConn); retryErr != nil {
					app.recordStartupError(retryErr)
				}
			}
			continue
		}
		database := result.database
		if database == nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(db.ErrDatabaseUnavailable)
				if app.databaseStartupPolicy == DatabaseStartupRequired {
					app.recordStartupError(fmt.Errorf("required default database connection %q unavailable: %w", spec.name, db.ErrDatabaseUnavailable))
				}
			}
			if app.log != nil {
				app.log.Warning(fmt.Sprintf("数据库连接 %q 未返回可用连接（启动策略=%s）", spec.name, app.databaseStartupPolicy))
			}
			continue
		}

		database.SetLocation(app.Location())
		database.SetLogger(app.log)
		app.loadDatabaseSchemaCache(spec, database)
		if err := app.dbManager.Add(spec.name, database); err != nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			closeErr := database.Close()
			app.recordStartupError(errors.Join(fmt.Errorf("database connection %q registration failed: %w", spec.name, err), closeErr))
			continue
		}
		if spec.name == defaultConn {
			app.db = database
			app.setDatabaseReadinessError(nil)
		}
	}
}

// databaseConnectionSpecs 在任何网络连接前完整校验命名连接配置。
func (app *App) databaseConnectionSpecs(dbConfigData map[string]interface{}, defaultConn string) []databaseConnectionSpec {
	rawConnections, exists := dbConfigData["connections"]
	connections, ok := rawConnections.(map[string]interface{})
	if !exists || !ok || len(connections) == 0 {
		app.setDatabaseReadinessError(fmt.Errorf("%w: database.connections 必须是非空对象", db.ErrInvalidDatabaseConfig))
		app.recordStartupError(fmt.Errorf("%w: database.connections 必须是非空对象", db.ErrInvalidDatabaseConfig))
		return nil
	}
	if _, exists := connections[defaultConn]; !exists {
		app.setDatabaseReadinessError(fmt.Errorf("%w: 默认连接 %q 未在 database.connections 中声明", db.ErrInvalidDatabaseConfig, defaultConn))
		app.recordStartupError(fmt.Errorf("%w: 默认连接 %q 未在 database.connections 中声明", db.ErrInvalidDatabaseConfig, defaultConn))
	}

	names := make([]string, 0, len(connections))
	for name := range connections {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]databaseConnectionSpec, 0, len(names))
	for _, name := range names {
		if err := db.ValidateConnectionName(name); err != nil {
			if name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			app.recordStartupError(fmt.Errorf("database connection %q invalid: %w", name, err))
			continue
		}
		connConfig, ok := connections[name].(map[string]interface{})
		if !ok {
			if name == defaultConn {
				app.setDatabaseReadinessError(db.ErrInvalidDatabaseConfig)
			}
			app.recordStartupError(fmt.Errorf("%w: 数据库连接 %q 配置必须是对象", db.ErrInvalidDatabaseConfig, name))
			continue
		}

		databaseConfig, err := readDatabaseConfig(connConfig)
		if err == nil {
			err = applyThinkPHPDatabaseDefaults(dbConfigData, &databaseConfig)
		}
		if err != nil {
			if name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			app.recordStartupError(fmt.Errorf("database connection %q invalid: %w", name, err))
			continue
		}
		applyDatabaseFallbacks(&databaseConfig)
		specs = append(specs, databaseConnectionSpec{name: name, config: databaseConfig})
	}
	return specs
}

// registerLazyDatabaseConnections 注册通过配置校验的连接工厂，默认连接首次访问后
// 才会真正拨号，保持 ThinkPHP 数据库管理器的按需连接行为。
func (app *App) registerLazyDatabaseConnections(dbConfigData map[string]interface{}, defaultConn string) bool {
	specs := app.databaseConnectionSpecs(dbConfigData, defaultConn)
	defaultRegistered := false
	for _, spec := range specs {
		current := spec
		err := app.registerDatabaseConnectionFactory(current, defaultConn)
		if err != nil {
			if current.name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			app.recordStartupError(fmt.Errorf("注册惰性数据库连接 %q 失败: %w", current.name, err))
			continue
		}
		if current.name == defaultConn {
			defaultRegistered = true
		}
	}
	return defaultRegistered
}

// registerDatabaseConnectionFactory 让首次连接故障保留可重试入口，支持无业务流量时由就绪探针恢复。
func (app *App) registerDatabaseConnectionFactory(spec databaseConnectionSpec, defaultConn string) error {
	return app.dbManager.RegisterFactory(spec.name, func() (*db.DB, error) {
		database, connectErr := connectDatabaseSafely(spec.config)
		if connectErr != nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(connectErr)
			}
			if app.log != nil {
				app.log.Warning(fmt.Sprintf("数据库连接 %q 失败（%s 策略将在下次访问或就绪检查时重试）: %s", spec.name, app.databaseStartupPolicy, redactDatabaseConnectionError(connectErr)))
			}
			return nil, connectErr
		}
		database.SetLocation(app.Location())
		database.SetLogger(app.log)
		app.loadDatabaseSchemaCache(spec, database)
		if spec.name == defaultConn {
			app.setDatabaseReadinessError(nil)
		}
		return database, nil
	})
}

// initializeDatabaseConnections 使用固定数量 worker，避免多个远程数据库的 Ping 让启动时间线性增长。
func initializeDatabaseConnections(specs []databaseConnectionSpec) map[string]databaseConnectionResult {
	results := make(map[string]databaseConnectionResult, len(specs))
	if len(specs) == 0 {
		return results
	}
	workerCount := len(specs)
	if workerCount > maxDatabaseInitializationConcurrency {
		workerCount = maxDatabaseInitializationConcurrency
	}
	jobs := make(chan databaseConnectionSpec)
	resultChannel := make(chan databaseConnectionResult, len(specs))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for index := 0; index < workerCount; index++ {
		go func() {
			defer workers.Done()
			for spec := range jobs {
				database, err := connectDatabaseSafely(spec.config)
				resultChannel <- databaseConnectionResult{name: spec.name, database: database, err: err}
			}
		}()
	}
	go func() {
		for _, spec := range specs {
			jobs <- spec
		}
		close(jobs)
		workers.Wait()
		close(resultChannel)
	}()
	for result := range resultChannel {
		results[result.name] = result
	}
	return results
}

// connectDatabaseSafely 将第三方连接器 panic 收敛为连接失败，避免后台 worker 直接崩溃宿主进程。
func connectDatabaseSafely(config db.Config) (database *db.DB, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			database = nil
			err = fmt.Errorf("数据库连接器 panic: %v", recovered)
		}
	}()
	return db.Connect(config)
}

func readDefaultDatabaseConnection(config map[string]interface{}) (string, error) {
	if len(config) == 0 {
		return "", fmt.Errorf("%w: database 配置不能为空", db.ErrInvalidDatabaseConfig)
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "default" && key != "connections" && key != "time_query_rule" &&
			key != "auto_timestamp" && key != "datetime_format" && key != "datetime_field" {
			return "", fmt.Errorf("%w: database 包含未知字段 %q", db.ErrInvalidDatabaseConfig, key)
		}
	}
	value, exists := config["default"]
	defaultConnection, ok := value.(string)
	if !exists || !ok || strings.TrimSpace(defaultConnection) != defaultConnection || defaultConnection == "" {
		return "", fmt.Errorf("%w: database.default 必须是非空字符串", db.ErrInvalidDatabaseConfig)
	}
	if err := db.ValidateConnectionName(defaultConnection); err != nil {
		return "", err
	}
	return defaultConnection, nil
}

// normalizeViewPath 规范化视图目录配置，避免空字符串触发越界，并统一相对路径基准。
// applyRouteConfig 将 route.json 的全部字段应用到路由器。
func (app *App) applyRouteConfig() {
	if app == nil {
		return
	}
	app.applyRouteConfigTo(app.config, app.route)
}

func (app *App) applyRouteConfigTo(configuration *config.Config, router *route.Router) {
	if app == nil || configuration == nil || router == nil {
		return
	}
	routeConfig := configuration.GetMap("route")
	if len(routeConfig) == 0 {
		return
	}

	allowed := map[string]struct{}{
		"pathinfo_depr":         {},
		"url_lazy_route":        {},
		"url_route_must":        {},
		"url_case_sensitive":    {},
		"route_auto_group":      {},
		"route_rule_merge":      {},
		"route_complete_match":  {},
		"default_route_pattern": {},
		"default_controller":    {},
		"default_action":        {},
		"default_module":        {},
		"empty_controller":      {},
		"controller_layer":      {},
		"url_html_suffix":       {},
		"remove_slash":          {},
		"controller_suffix":     {},
		"action_suffix":         {},
		"action_bind_param":     {},
		"url_common_param":      {},
		"request_cache_key":     {},
		"request_cache_expire":  {},
		"request_cache_except":  {},
		"request_cache_tag":     {},
		"api_version":           {},
	}
	keys := make([]string, 0, len(routeConfig))
	for key := range routeConfig {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := allowed[key]; !exists {
			app.recordStartupError(fmt.Errorf("route 配置包含未知字段 %q", key))
		}
	}

	caseSensitive, err := readRouteBool(routeConfig, "url_case_sensitive", false)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.SetCaseSensitive(caseSensitive); err != nil {
		app.recordStartupError(fmt.Errorf("设置 URL 大小写规则失败: %w", err))
	}
	completeMatch, err := readRouteBool(routeConfig, "route_complete_match", false)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.SetCompleteMatch(completeMatch); err != nil {
		app.recordStartupError(fmt.Errorf("设置路由完整匹配规则失败: %w", err))
	}
	removeSlash, err := readRouteBool(routeConfig, "remove_slash", false)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.SetRemoveSlash(removeSlash); err != nil {
		app.recordStartupError(fmt.Errorf("设置路由末尾斜杠规则失败: %w", err))
	}
	if _, err := readRouteBool(routeConfig, "controller_suffix", false); err != nil {
		app.recordStartupError(err)
	}
	if raw, exists := routeConfig["action_suffix"]; exists {
		if _, ok := raw.(string); !ok {
			app.recordStartupError(fmt.Errorf("route.action_suffix 必须是字符串"))
		}
	}
	if raw, exists := routeConfig["action_bind_param"]; exists {
		mode, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.action_bind_param 必须是字符串"))
		} else {
			switch strings.ToLower(strings.TrimSpace(mode)) {
			case "route", "get", "param":
			default:
				app.recordStartupError(fmt.Errorf("route.action_bind_param 必须是 route、get 或 param"))
			}
		}
	}

	if raw, exists := routeConfig["url_html_suffix"]; exists {
		extension, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.url_html_suffix 必须是字符串"))
		} else if err := router.SetDefaultExtension(extension); err != nil {
			app.recordStartupError(fmt.Errorf("设置 URL 后缀失败: %w", err))
		}
	}
	if raw, exists := routeConfig["default_route_pattern"]; exists {
		pattern, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.default_route_pattern 必须是字符串"))
		} else if err := router.SetDefaultPattern(pattern); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由变量规则失败: %w", err))
		}
	}
	if raw, exists := routeConfig["controller_layer"]; exists {
		layer, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.controller_layer 必须是字符串"))
		} else if err := router.SetControllerLayer(layer); err != nil {
			app.recordStartupError(fmt.Errorf("设置控制器层失败: %w", err))
		}
	}
	if raw, exists := routeConfig["default_controller"]; exists {
		controller, ok := raw.(string)
		if !ok || strings.TrimSpace(controller) == "" {
			app.recordStartupError(fmt.Errorf("route.default_controller 必须是非空字符串"))
		} else if err := router.SetDefaultController(controller); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由控制器失败: %w", err))
		}
	}
	if raw, exists := routeConfig["default_action"]; exists {
		action, ok := raw.(string)
		if !ok || strings.TrimSpace(action) == "" {
			app.recordStartupError(fmt.Errorf("route.default_action 必须是非空字符串"))
		} else if err := router.SetDefaultAction(action); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由动作失败: %w", err))
		}
	}
	mustRoute, err := readRouteBool(routeConfig, "url_route_must", false)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.EnableAutoRoute(!mustRoute); err != nil {
		app.recordStartupError(fmt.Errorf("设置自动路由开关失败: %w", err))
	}
}

// applyMiddlewareConfig 将 middleware.json 的 alias 和 priority 接入全局管道。
// Go 中间件不能从 JSON 反射构造，因此 alias 的值定义为已注册别名，并支持别名链。
func (app *App) applyMiddlewareConfig() {
	if app == nil {
		return
	}
	app.applyMiddlewareConfigTo(app.config, app.middleware)
}

func (app *App) applyMiddlewareConfigTo(configuration *config.Config, pipeline *middleware.Pipeline) {
	if app == nil || configuration == nil || pipeline == nil {
		return
	}
	values := configuration.GetMap("middleware")
	if len(values) == 0 {
		return
	}
	for key := range values {
		if key != "alias" && key != "priority" {
			app.recordStartupError(fmt.Errorf("middleware 配置包含未知字段 %q", key))
		}
	}

	if rawAliases, exists := values["alias"]; exists {
		aliases, ok := rawAliases.(map[string]interface{})
		if !ok {
			app.recordStartupError(fmt.Errorf("middleware.alias 必须是对象"))
		} else {
			app.applyMiddlewareAliasesTo(pipeline, aliases)
		}
	}
	if rawPriority, exists := values["priority"]; exists {
		var priority []interface{}
		ok := true
		switch typed := rawPriority.(type) {
		case []interface{}:
			priority = typed
		case []string:
			priority = make([]interface{}, len(typed))
			for index, name := range typed {
				priority[index] = name
			}
		default:
			ok = false
		}
		if !ok {
			app.recordStartupError(fmt.Errorf("middleware.priority 必须是字符串数组"))
			return
		}
		names := make([]string, 0, len(priority))
		seen := make(map[string]struct{}, len(priority))
		for index, rawName := range priority {
			name, ok := rawName.(string)
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				app.recordStartupError(fmt.Errorf("middleware.priority[%d] 必须是非空字符串", index))
				continue
			}
			if _, exists := seen[name]; exists {
				app.recordStartupError(fmt.Errorf("middleware.priority 包含重复别名 %q", name))
				continue
			}
			if pipeline.ResolveAlias(name) == nil {
				app.recordStartupError(fmt.Errorf("middleware.priority 引用了未注册别名 %q", name))
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
		pipeline.SetPriority(names)
	}
}

func (app *App) applyMiddlewareAliasesTo(pipeline *middleware.Pipeline, values map[string]interface{}) {
	if app == nil || pipeline == nil {
		return
	}
	pending := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	for alias, rawTarget := range values {
		alias = strings.TrimSpace(alias)
		target, ok := rawTarget.(string)
		target = strings.TrimSpace(target)
		if alias == "" || target == "" {
			app.recordStartupError(fmt.Errorf("middleware.alias 包含空别名或空目标"))
			continue
		}
		if strings.ContainsAny(alias, "\r\n\x00") || strings.ContainsAny(target, "\r\n\x00") {
			app.recordStartupError(fmt.Errorf("middleware.alias %q 包含控制字符", alias))
			continue
		}
		if !ok {
			app.recordStartupError(fmt.Errorf("middleware.alias[%q] 必须是已注册别名字符串", alias))
			continue
		}
		pending[alias] = target
		keys = append(keys, alias)
	}
	sort.Strings(keys)

	for len(pending) > 0 {
		progress := false
		for _, alias := range keys {
			target, exists := pending[alias]
			if !exists {
				continue
			}
			handler := pipeline.ResolveAlias(target)
			if handler == nil {
				continue
			}
			pipeline.Alias(alias, handler)
			delete(pending, alias)
			progress = true
		}
		if progress {
			continue
		}
		for _, alias := range keys {
			if target, exists := pending[alias]; exists {
				app.recordStartupError(fmt.Errorf("middleware.alias %q 的目标 %q 未注册或存在循环引用", alias, target))
			}
		}
		return
	}
}

// readRouteBool 读取路由布尔字段，兼容 ThinkPHP 常见的 true、false、1 和 0 写法。
func readRouteBool(values map[string]interface{}, name string, defaultValue bool) (bool, error) {
	raw, exists := values[name]
	if !exists {
		return defaultValue, nil
	}
	switch value := raw.(type) {
	case bool:
		return value, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		}
	}
	return defaultValue, fmt.Errorf("route.%s 必须是布尔值", name)
}

func normalizeViewPath(basePath string, viewConfig map[string]interface{}) {
	viewPath, ok := viewConfig["view_path"].(string)
	if !ok {
		return
	}

	viewPath = strings.TrimSpace(viewPath)
	viewConfig["view_path"] = viewPath
	if viewPath == "" {
		return
	}

	if !os.IsPathSeparator(viewPath[0]) && viewPath[0] != '.' {
		viewConfig["view_path"] = basePath + "/" + viewPath
	}
}
