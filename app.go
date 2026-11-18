package framework

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/health"
	"github.com/zhuhanxin0308/thinkgo/v3/lang"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/migration"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
)

// App 应用实例，按单一职责组合元数据、托管服务、启动状态、注册表、运行策略和数据库状态。
// 对应 ThinkPHP 8 的 think\App
type App struct {
	BasePath        string
	AppName         string
	ApplicationPath string
	RuntimePath     string
	DebugMode       bool
	Kernel          Kernel

	appMetadataState
	appServiceHolders
	appStartupState
	appRegistrationState
	appRuntimePolicyState
	appDatabaseRuntimeState
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
		BasePath:        path,
		AppName:         defaultApplicationDisplayName,
		ApplicationPath: applicationPath,
		RuntimePath:     runtimePath,
		appMetadataState: appMetadataState{
			nativeRuntimeBasePath: runtimePath,
			namespace:             "app",
			configExt:             ".json",
			consoleMode:           skipDatabase,
			location:              defaultApplicationLocation(),
		},
		appServiceHolders: appServiceHolders{
			container:             NewContainer(),
			route:                 router,
			middleware:            middleware.NewPipeline(),
			applicationMiddleware: middleware.NewPipeline(),
			config:                config.NewConfig(),
			env:                   env.NewEnv(),
			// 构造阶段只保留无驱动日志占位器，真实日志驱动由 Log Provider 按配置创建。
			log:        log.NewLog(),
			metrics:    metrics.NewRegistry(),
			health:     health.NewRegistry(),
			event:      event.NewDispatcher(),
			lang:       lang.NewLang(),
			debug:      debug.NewDebug(),
			migrations: migration.NewRegistry(),
			telemetry:  telemetry.Disabled(),
		},
		appStartupState: appStartupState{
			lifecycle: appLifecycle{requiresInitialization: true, state: ApplicationStateConstructed},
		},
		appDatabaseRuntimeState: appDatabaseRuntimeState{
			skipDatabaseInit: skipDatabase,
		},
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

// Environment 返回当前运行环境；配置未显式声明时回退读取 APP_APP_ENV / APP_ENV / env_name。
func (app *App) Environment() string {
	if app == nil {
		return ""
	}
	hasConfiguredAppEnv := false
	environmentName := ""
	if app.config != nil {
		hasConfiguredAppEnv = app.config.Has("app.app_env")
		environmentName = strings.TrimSpace(app.config.GetString("app.app_env"))
	}
	if environmentName == "" {
		_, configuredName := app.environmentNames()
		environmentName = strings.TrimSpace(configuredName)
	}
	if environmentName == "" && !hasConfiguredAppEnv {
		if app.env != nil {
			if value, ok := app.env.Lookup("APP_APP_ENV"); ok {
				environmentName = strings.TrimSpace(value)
			} else if value, ok := app.env.Lookup("APP_ENV"); ok {
				environmentName = strings.TrimSpace(value)
			}
		} else {
			if value, ok := os.LookupEnv("APP_APP_ENV"); ok {
				environmentName = strings.TrimSpace(value)
			} else if value, ok := os.LookupEnv("APP_ENV"); ok {
				environmentName = strings.TrimSpace(value)
			}
		}
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
