package framework

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"thinkgo/framework/config"
	"thinkgo/framework/db"
	_ "thinkgo/framework/db/connector" // 注册全部 SQL 驱动
	"time"
	// _ "thinkgo/framework/db/connector/mongo" // 按需注册 MongoDB 驱动
	// _ "thinkgo/framework/db/connector/neo4j" // 按需注册 Neo4j 驱动
	"thinkgo/framework/cache"
	"thinkgo/framework/cookie"
	"thinkgo/framework/debug"
	"thinkgo/framework/env"
	"thinkgo/framework/event"
	"thinkgo/framework/lang"
	"thinkgo/framework/log"
	logDriver "thinkgo/framework/log/driver"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
	"thinkgo/framework/session"
	"thinkgo/framework/view"
	"thinkgo/framework/view/driver"
)

// App 应用实例
// 对应 ThinkPHP 8 的 think\App
type App struct {
	*Container
	BasePath            string
	DebugMode           bool
	Route               *route.Router
	Middleware          *middleware.Pipeline
	DB                  *db.DB
	DBManager           *db.Manager
	Config              *config.Config
	Env                 *env.Env
	Log                 *log.Log
	View                *view.View
	Cache               *cache.Cache
	Event               *event.Dispatcher
	Lang                *lang.Lang
	Cookie              *cookie.Cookie
	Session             *session.Session
	Debug               *debug.Debug
	Kernel              Kernel
	startupMu           sync.RWMutex
	startupErr          error
	startupErrorCount   int
	startupErrorOmitted int
	initializeOnce      sync.Once
	providers           providerLifecycle
	lifecycle           appLifecycle
	sessionGCStop       func() // 停止后台会话回收协程
	// skipDatabaseInit 为 true 时，Initialize 跳过数据库连接。
	// 供不需要数据库的控制台命令（version/list/make:* 等）使用，
	// 避免每次执行命令都尝试连库并打印连接失败日志。
	skipDatabaseInit bool
}

// NewApp 创建完整初始化的应用实例。
func NewApp(basePath ...string) *App {
	return newApp(false, basePath...)
}

// NewConsoleApp 创建用于控制台命令的应用实例，跳过数据库连接。
// version/list/make:* 等命令不需要数据库；需要数据库的命令（如 run）应使用 NewApp。
func NewConsoleApp(basePath ...string) *App {
	return newApp(true, basePath...)
}

// newApp 构建并初始化应用实例，skipDatabase 控制是否跳过数据库连接。
func newApp(skipDatabase bool, basePath ...string) *App {
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

	app := &App{
		Container:        NewContainer(),
		BasePath:         path,
		Route:            route.NewRouter(),
		Middleware:       middleware.NewPipeline(),
		Config:           config.NewConfig(),
		Env:              env.NewEnv(),
		Log:              log.NewLog(logDriver.NewFile(path + RuntimeLogDir)),
		Event:            event.NewDispatcher(),
		Lang:             lang.NewLang(),
		Debug:            debug.NewDebug(),
		skipDatabaseInit: skipDatabase,
	}
	for _, constructionErr := range constructionErrors {
		app.recordStartupError(constructionErr)
	}
	_ = app.Initialize()

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
	return app.DebugMode
}

// Environment 返回当前运行环境，默认 production。
func (app *App) Environment() string {
	if app == nil || app.Env == nil {
		return "production"
	}
	environmentName := strings.TrimSpace(app.Env.Get("APP_ENV"))
	if environmentName == "" {
		return "production"
	}
	return environmentName
}

// ==================== URL生成方法 ====================

// Domain 获取服务器域名
func (app *App) Domain() string {
	if app == nil || app.Env == nil {
		return "https://localhost"
	}
	domain := strings.TrimSpace(app.Env.Get("SERVER_DOMAIN", "https://localhost"))
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
	// 1. Load .env
	if err := app.Env.Load(app.BasePath + "/.env"); err != nil {
		app.recordStartupError(fmt.Errorf("load environment failed: %w", err))
	}

	// 2. Load config files
	if err := app.Config.LoadAll(app.BasePath + "/config"); err != nil {
		app.recordStartupError(fmt.Errorf("load config failed: %w", err))
	}

	// 3. Override with Env
	// 应用配置覆盖。
	app.applyBooleanEnvironmentOverride("APP_DEBUG", "app.app_debug")
	app.applyBooleanEnvironmentOverride("APP_TRACE", "app.app_trace")
	app.DebugMode = app.Config.GetBool("app.app_debug", false)
	app.Debug.Enabled = app.Config.GetBool("app.app_trace", false)

	// 服务器配置覆盖。
	// 通过点路径 Set 覆盖，所有写入经由持锁的 Set 完成，
	// 不再依赖修改 Get 返回的内部 map 引用（Set 会自动创建缺失的 tls 子树）。
	if val := app.Env.Get("SERVER_HOST"); val != "" {
		app.Config.Set("app.server.host", val)
	}
	if val := app.Env.Get("SERVER_PORT"); val != "" {
		// 环境变量覆盖端口配置（字符串类型，由 Http.parseConfig 处理类型转换）
		app.Config.Set("app.server.port", val)
	}
	app.applyBooleanEnvironmentOverride("SERVER_HTTP3", "app.server.http3")

	// TLS 配置覆盖。
	app.applyBooleanEnvironmentOverride("SERVER_TLS_ENABLE", "app.server.tls.enable")
	if val := app.Env.Get("SERVER_TLS_CERT"); val != "" {
		app.Config.Set("app.server.tls.cert_file", val)
	}
	if val := app.Env.Get("SERVER_TLS_KEY"); val != "" {
		app.Config.Set("app.server.tls.key_file", val)
	}

	// 3.5 Init Log
	logConfig := app.Config.GetMap("log")
	defaultChannel := "file"
	if rawDefault, exists := logConfig["default"]; exists {
		if value, ok := rawDefault.(string); ok && strings.TrimSpace(value) != "" {
			defaultChannel = strings.TrimSpace(value)
		} else {
			app.recordStartupError(fmt.Errorf("日志配置 default 必须是非空字符串"))
		}
	}

	channels, channelsValid := logConfig["channels"].(map[string]interface{})
	if !channelsValid {
		app.recordStartupError(fmt.Errorf("日志配置 channels 必须是对象"))
	} else {
		rawDefaultChannel, exists := channels[defaultChannel]
		channelConfig, configValid := rawDefaultChannel.(map[string]interface{})
		if !exists || !configValid {
			app.recordStartupError(fmt.Errorf("默认日志通道 %q 不存在或配置无效", defaultChannel))
		} else {
			configuredLog, err := createAppLogChannel(app, channelConfig, app.DebugMode)
			if err != nil {
				app.recordStartupError(fmt.Errorf("初始化默认日志通道 %q 失败: %w", defaultChannel, err))
			} else {
				previousLog := app.Log
				app.Log = configuredLog
				if previousLog != nil && previousLog != configuredLog {
					if err := previousLog.Close(); err != nil {
						app.recordStartupError(fmt.Errorf("关闭初始日志通道失败: %w", err))
					}
				}
				app.Instance("log", app.Log)
			}
		}
	}
	if channelsValid && app.Log != nil {
		for name, rawChannelConfig := range channels {
			if name == defaultChannel {
				continue
			}
			channelConfig, ok := rawChannelConfig.(map[string]interface{})
			if !ok {
				app.recordStartupError(fmt.Errorf("日志通道 %q 的配置必须是对象", name))
				continue
			}
			channel, err := createAppLogChannel(app, channelConfig, false)
			if err != nil {
				app.recordStartupError(fmt.Errorf("初始化日志通道 %q 失败: %w", name, err))
				continue
			}
			if err := app.Log.RegisterChannel(name, channel); err != nil {
				_ = channel.Close()
				app.recordStartupError(fmt.Errorf("注册日志通道 %q 失败: %w", name, err))
			}
		}
	}

	// 4. Init Lang（从 config/lang.json 读取完整多语言配置）
	langConfig := app.Config.GetMap("lang")
	// 兼容旧配置：如果 lang 配置没有 default_lang，从 app 配置读取
	if _, ok := langConfig["default_lang"]; !ok {
		langConfig["default_lang"] = app.Config.Get("app.default_lang", "zh-cn")
	}
	if err := app.Lang.Init(langConfig); err != nil {
		app.recordStartupError(fmt.Errorf("初始化多语言配置失败: %w", err))
	} else if err := app.Lang.LoadAll(app.BasePath + "/app/lang"); err != nil {
		app.recordStartupError(fmt.Errorf("加载语言文件失败: %w", err))
	}
	// 5. Init Cache
	cacheConfig := app.Config.GetMap("cache")
	configuredCache, err := createAppCache(app, cacheConfig)
	if err != nil {
		app.recordStartupError(fmt.Errorf("初始化缓存失败: %w", err))
		// 保留非空服务实例，但不安装隐式驱动；任何缓存调用都会返回明确错误。
		configuredCache = cache.NewCache(app.Debug, nil)
	}
	app.Cache = configuredCache
	app.Instance("cache", app.Cache)

	// 6. Init View
	viewConfig := app.Config.GetMap("view")
	normalizeViewPath(app.BasePath, viewConfig)
	app.View = view.NewView(app.Debug, viewConfig)
	if err := app.View.SetDriver(driver.NewGoTemplate()); err != nil {
		app.recordStartupError(fmt.Errorf("初始化视图驱动失败: %w", err))
	} else {
		// 仅在驱动成功安装后注册函数，避免同一根因产生重复启动错误。
		if err := app.View.SetFuncMap(map[string]interface{}{
			"lang": func(key string) string {
				return app.Lang.Get(key, nil, "")
			},
		}); err != nil {
			app.recordStartupError(fmt.Errorf("注册视图函数失败: %w", err))
		}
	}

	app.Instance("view", app.View)

	// 7. Init Cookie
	cookieConfig := app.Config.GetMap("cookie")
	app.Cookie, err = createAppCookie(cookieConfig)
	if err != nil {
		app.recordStartupError(fmt.Errorf("初始化 Cookie 失败: %w", err))
		app.Cookie = fallbackAppCookie()
	}
	app.Instance("cookie", app.Cookie)

	// 8. Init Session
	sessionConfig := app.Config.GetMap("session")
	app.Session, err = createAppSession(app, sessionConfig, app.Cookie)
	if err != nil {
		app.recordStartupError(fmt.Errorf("初始化 Session 失败: %w", err))
		app.Session = fallbackAppSession(app.Cookie)
	}
	app.Session.SetLogger(app.Log)
	app.Instance("session", app.Session)

	// 9. Init Database
	dbConfigData := app.Config.GetMap("database")
	defaultConn, databaseRootErr := readDefaultDatabaseConnection(dbConfigData)
	if databaseRootErr != nil {
		app.recordStartupError(databaseRootErr)
		defaultConn = "default"
	}
	app.DBManager = db.NewManager(defaultConn)

	// 控制台命令可跳过数据库连接（version/list/make:* 等不依赖数据库）。
	if !app.skipDatabaseInit && databaseRootErr == nil {
		app.initDatabaseConnections(dbConfigData, defaultConn)
	}

	// 10. 注册控制器类型到容器（使用工厂模式，避免并发请求复用同一实例）
	for name, controllerType := range snapshotControllerRegistry() {
		app.BindFactory(name, controllerType)
	}

	// 11. Register Global Middleware
	recovery := &middleware.Recovery{
		App:    app,
		Log:    app.Log,
		TplDir: app.BasePath + "/framework/exception/tpl",
	}
	app.Middleware.Pipe(recovery.Handle)

	// 按配置注册 Session 中间件和回收任务。
	if app.Config.GetBool("app.session_enable", false) {
		sessionMiddleware := &middleware.Session{Manager: app.Session}
		app.Middleware.Pipe(sessionMiddleware.Handle)
		// 启动后台会话回收，避免文件型会话在磁盘无限堆积。
		app.sessionGCStop = app.Session.StartGarbageCollector(time.Hour)
	}

	// 按配置注册 Trace 中间件。
	if app.Config.GetBool("app.app_trace", false) {
		trace := &middleware.Trace{Debug: app.Debug}
		app.Middleware.Pipe(trace.Handle)
	}

	// CSRF：注册别名供路由/控制器按需启用；当 app.csrf_enable=true 时对全局生效。
	csrfHandler, csrfErr := createAppCSRF(app.Config.GetMap("csrf"), app.Cookie)
	if csrfErr != nil {
		app.recordStartupError(fmt.Errorf("初始化 CSRF 失败: %w", csrfErr))
		csrfHandler = unavailableCSRFHandler
	}
	app.Middleware.Alias("csrf", csrfHandler)
	if app.Config.GetBool("app.csrf_enable", false) {
		app.Middleware.PipeByName("csrf")
	}

	// 注册请求语言解析中间件。
	app.Middleware.Pipe(app.LoadLangPack())

	// 追加应用层全局中间件快照。
	for _, handler := range snapshotMiddlewareRegistry() {
		app.Middleware.Pipe(handler)
	}

	// 11.5 应用路由配置（对应 ThinkPHP 的 config/route.php）。
	// 默认 url_route_must=true（安全基线，仅显式路由）；显式设为 false 才开启自动路由。
	app.applyRouteConfig()

	// 12. Load Routes
	for _, loader := range snapshotRouteRegistry() {
		if err := safeLoadRoutes(loader, app); err != nil {
			app.recordStartupError(fmt.Errorf("加载应用路由失败: %w", err))
		}
	}
}

// initDatabaseConnections 按配置建立数据库连接，并把默认连接绑定到 app.DB。
func (app *App) initDatabaseConnections(dbConfigData map[string]interface{}, defaultConn string) {
	rawConnections, exists := dbConfigData["connections"]
	connections, ok := rawConnections.(map[string]interface{})
	if !exists || !ok || len(connections) == 0 {
		app.recordStartupError(fmt.Errorf("%w: database.connections 必须是非空对象", db.ErrInvalidDatabaseConfig))
		return
	}
	if _, exists := connections[defaultConn]; !exists {
		app.recordStartupError(fmt.Errorf("%w: 默认连接 %q 未在 database.connections 中声明", db.ErrInvalidDatabaseConfig, defaultConn))
	}

	names := make([]string, 0, len(connections))
	for name := range connections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := db.ValidateConnectionName(name); err != nil {
			app.recordStartupError(fmt.Errorf("database connection %q invalid: %w", name, err))
			continue
		}
		connConfig, ok := connections[name].(map[string]interface{})
		if !ok {
			app.recordStartupError(fmt.Errorf("%w: 数据库连接 %q 配置必须是对象", db.ErrInvalidDatabaseConfig, name))
			continue
		}

		databaseConfig, err := readDatabaseConfig(connConfig)
		if err != nil {
			app.recordStartupError(fmt.Errorf("database connection %q invalid: %w", name, err))
			continue
		}
		if name == defaultConn {
			if err := applyDatabaseEnvOverrides(app, &databaseConfig); err != nil {
				app.recordStartupError(fmt.Errorf("database connection %q environment invalid: %w", name, err))
				continue
			}
		}
		applyDatabaseFallbacks(&databaseConfig)

		database, err := db.Connect(databaseConfig)
		if err != nil {
			// 数据库连接失败不阻塞应用启动——仅记录警告，访问时按需报错。
			app.Log.Warning(fmt.Sprintf("数据库连接 %q 失败（应用仍可启动，但使用数据库的功能不可用）: %v", name, err))
			continue
		}
		database.SetLogger(app.Log)
		if err := app.DBManager.Add(name, database); err != nil {
			closeErr := database.Close()
			app.recordStartupError(errors.Join(fmt.Errorf("database connection %q registration failed: %w", name, err), closeErr))
			continue
		}
		if name == defaultConn {
			app.DB = database
		}
	}
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
		if key != "default" && key != "connections" {
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

// applyRouteConfig 把 config/route.json 配置接入路由器。
// 此前该配置被加载但从未被消费，导致 url_route_must / default_controller / default_action 形同摆设。
func (app *App) applyRouteConfig() {
	routeConfig := app.Config.GetMap("route")
	if len(routeConfig) == 0 {
		return
	}

	if controller, ok := routeConfig["default_controller"].(string); ok && controller != "" {
		if err := app.Route.SetDefaultController(controller); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由控制器失败: %w", err))
		}
	}
	if action, ok := routeConfig["default_action"].(string); ok && action != "" {
		if err := app.Route.SetDefaultAction(action); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由动作失败: %w", err))
		}
	}

	// 安全基线：默认仅允许显式注册的路由（mustRoute=true）。
	// 只有显式配置 url_route_must=false 才开启 URL 自动解析（/控制器/动作 → Controller@Action），
	// 避免配置缺失时 fail-open 把所有导出方法暴露成端点。
	mustRoute := true
	switch v := routeConfig["url_route_must"].(type) {
	case bool:
		mustRoute = v
	case string:
		mustRoute = !(v == "false" || v == "0")
	}
	if err := app.Route.EnableAutoRoute(!mustRoute); err != nil {
		app.recordStartupError(fmt.Errorf("设置自动路由开关失败: %w", err))
	}
}

// normalizeViewPath 规范化视图目录配置，避免空字符串触发越界，并统一相对路径基准。
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

// applyBooleanEnvironmentOverride 严格解析布尔环境变量，存在但非法时记录启动错误。
func (app *App) applyBooleanEnvironmentOverride(environmentKey string, configPath string) {
	if _, exists := app.Env.Lookup(environmentKey); !exists {
		return
	}
	value, err := app.Env.GetBool(environmentKey)
	if err != nil {
		app.recordStartupError(err)
		return
	}
	app.Config.Set(configPath, value)
}

// applyDatabaseEnvOverrides 用环境变量覆盖数据库配置，避免部署环境必须改配置文件。
func applyDatabaseEnvOverrides(app *App, dbConfig *db.Config) error {
	if app == nil || dbConfig == nil {
		return fmt.Errorf("%w: 应用或数据库配置为空", db.ErrInvalidDatabaseConfig)
	}
	working := *dbConfig

	if val := app.Env.Get("DB_TYPE"); val != "" {
		working.Type = val
	}
	if val := app.Env.Get("DB_HOST"); val != "" {
		working.Hostname = val
	}
	if val := app.Env.Get("DB_PORT"); val != "" {
		working.Hostport = val
	}
	if val := app.Env.Get("DB_USER"); val != "" {
		working.Username = val
	}
	if val, ok := app.Env.Lookup("DB_PASS"); ok {
		working.Password = val
	}
	if val := app.Env.Get("DB_NAME"); val != "" {
		working.Database = val
	}
	integerOverrides := []struct {
		name   string
		target *int
	}{
		{name: "DB_MAX_OPEN_CONNS", target: &working.MaxOpenConns},
		{name: "DB_MAX_IDLE_CONNS", target: &working.MaxIdleConns},
		{name: "DB_CONN_MAX_LIFETIME_SECONDS", target: &working.ConnMaxLifetimeSeconds},
		{name: "DB_CONN_MAX_IDLE_TIME_SECONDS", target: &working.ConnMaxIdleTimeSeconds},
	}
	for _, override := range integerOverrides {
		if raw := app.Env.Get(override.name); raw != "" {
			value, err := readEnvNonnegativeInt(override.name, raw)
			if err != nil {
				return err
			}
			*override.target = value
		}
	}
	if val := app.Env.Get("DB_TIMESTAMP_VALUE_TYPE"); val != "" {
		working.TimestampValueType = val
	}
	*dbConfig = working
	return nil
}

func readDatabaseConfig(connConfig map[string]interface{}) (db.Config, error) {
	allowedFields := map[string]bool{
		"type": true, "hostname": true, "hostport": true, "database": true,
		"username": true, "password": true, "charset": true, "prefix": true,
		"debug": true, "auto_timestamp": true, "create_time_field": true,
		"update_time_field": true, "timestamp_value_type": true,
		"max_open_conns": true, "max_idle_conns": true,
		"conn_max_lifetime_seconds": true, "conn_max_idle_time_seconds": true,
		"params": true,
	}
	keys := make([]string, 0, len(connConfig))
	for key := range connConfig {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowedFields[key] {
			return db.Config{}, fmt.Errorf("%w: 未知数据库配置字段 %q", db.ErrInvalidDatabaseConfig, key)
		}
	}

	config := db.Config{}
	stringFields := []struct {
		name   string
		target *string
	}{
		{name: "type", target: &config.Type},
		{name: "hostname", target: &config.Hostname},
		{name: "hostport", target: &config.Hostport},
		{name: "database", target: &config.Database},
		{name: "username", target: &config.Username},
		{name: "password", target: &config.Password},
		{name: "charset", target: &config.Charset},
		{name: "prefix", target: &config.Prefix},
		{name: "create_time_field", target: &config.CreateTimeField},
		{name: "update_time_field", target: &config.UpdateTimeField},
		{name: "timestamp_value_type", target: &config.TimestampValueType},
	}
	for _, field := range stringFields {
		value, exists := connConfig[field.name]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return db.Config{}, fmt.Errorf("%w: %s 必须是字符串", db.ErrInvalidDatabaseConfig, field.name)
		}
		*field.target = text
	}
	booleanFields := []struct {
		name   string
		target *bool
	}{
		{name: "debug", target: &config.Debug},
		{name: "auto_timestamp", target: &config.AutoTimestamp},
	}
	for _, field := range booleanFields {
		value, exists := connConfig[field.name]
		if !exists {
			continue
		}
		boolean, ok := value.(bool)
		if !ok {
			return db.Config{}, fmt.Errorf("%w: %s 必须是布尔值", db.ErrInvalidDatabaseConfig, field.name)
		}
		*field.target = boolean
	}
	integerFields := []struct {
		name   string
		target *int
	}{
		{name: "max_open_conns", target: &config.MaxOpenConns},
		{name: "max_idle_conns", target: &config.MaxIdleConns},
		{name: "conn_max_lifetime_seconds", target: &config.ConnMaxLifetimeSeconds},
		{name: "conn_max_idle_time_seconds", target: &config.ConnMaxIdleTimeSeconds},
	}
	for _, field := range integerFields {
		value, exists := connConfig[field.name]
		if !exists {
			continue
		}
		parsed, err := readConfigIntValue(value)
		if err != nil {
			return db.Config{}, fmt.Errorf("%w: %s: %w", db.ErrInvalidDatabaseConfig, field.name, err)
		}
		*field.target = parsed
	}
	params, err := readDatabaseParams(connConfig["params"])
	if err != nil {
		return db.Config{}, err
	}
	config.Params = params
	applyDatabaseFallbacks(&config)
	if err := config.Validate(); err != nil {
		return db.Config{}, err
	}
	return config, nil
}

// readDatabaseParams 要求 JSON 连接参数显式使用字符串，避免布尔值或浮点数被隐式改写。
func readDatabaseParams(raw interface{}) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	rawParams, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: params 必须是对象", db.ErrInvalidDatabaseConfig)
	}
	if len(rawParams) == 0 {
		return nil, nil
	}

	params := make(map[string]string, len(rawParams))
	keys := make([]string, 0, len(rawParams))
	for key := range rawParams {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%w: params 包含空键", db.ErrInvalidDatabaseConfig)
		}
		value, ok := rawParams[key].(string)
		if !ok {
			return nil, fmt.Errorf("%w: params.%s 必须是字符串", db.ErrInvalidDatabaseConfig, key)
		}
		params[key] = value
	}
	return params, nil
}

func applyDatabaseFallbacks(dbConfig *db.Config) {
	if dbConfig.TimestampValueType == "" {
		dbConfig.TimestampValueType = db.TimestampValueTypeUnix
	}
}

// readConfigIntValue 读取 JSON 非负整数；浮点表示超出精确范围时拒绝。
func readConfigIntValue(raw interface{}) (int, error) {
	maximum := uint64(maxIntValue())
	var value uint64
	switch typed := raw.(type) {
	case int:
		if typed < 0 {
			return 0, fmt.Errorf("不能为负数")
		}
		value = uint64(typed)
	case int64:
		if typed < 0 {
			return 0, fmt.Errorf("不能为负数")
		}
		value = uint64(typed)
	case uint:
		value = uint64(typed)
	case uint64:
		value = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || math.Trunc(typed) != typed || typed > 1<<53 {
			return 0, fmt.Errorf("必须是可精确表示的非负整数")
		}
		value = uint64(typed)
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("必须是非负整数: %w", err)
		}
		value = parsed
	default:
		return 0, fmt.Errorf("类型 %T 非法", raw)
	}
	if value > maximum {
		return 0, fmt.Errorf("超出当前平台 int 范围")
	}
	return int(value), nil
}

func readEnvNonnegativeInt(name, raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: %s 必须是非负整数", db.ErrInvalidDatabaseConfig, name)
	}
	return value, nil
}
