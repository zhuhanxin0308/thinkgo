package framework

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
	"thinkgo/framework/config"
	"thinkgo/framework/db"
	_ "thinkgo/framework/db/connector" // Register all drivers
	// _ "thinkgo/framework/db/connector/mongo" // Register MongoDB driver
	// _ "thinkgo/framework/db/connector/neo4j" // Register Neo4j driver
	"thinkgo/framework/cache"
	cacheDriver "thinkgo/framework/cache/driver"
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
	sessionDriver "thinkgo/framework/session/driver"
	"thinkgo/framework/view"
	"thinkgo/framework/view/driver"
)

// ControllerRegistry 控制器类型注册表（存储 reflect.Type，每次请求创建新实例）
var ControllerRegistry = make(map[string]reflect.Type)

// RegisterController 注册控制器（提取类型信息，而非存储实例）
// 每次请求时通过 reflect.New() 创建新实例，避免并发时状态覆盖
func RegisterController(name string, controller interface{}) {
	t := reflect.TypeOf(controller)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	ControllerRegistry[name] = t
}

// RouteRegistry stores registered route loaders
var RouteRegistry = []func(app *App){}

// RegisterRouteLoader registers a route loader
func RegisterRouteLoader(loader func(app *App)) {
	RouteRegistry = append(RouteRegistry, loader)
}

// MiddlewareRegistry stores registered global middleware
var MiddlewareRegistry = []middleware.Handler{}

// RegisterGlobalMiddleware registers a global middleware
func RegisterGlobalMiddleware(handler middleware.Handler) {
	MiddlewareRegistry = append(MiddlewareRegistry, handler)
}

// App 应用实例
// 对应 ThinkPHP 8 的 think\App
type App struct {
	*Container
	BasePath   string
	DebugMode  bool
	Route      *route.Router
	Middleware *middleware.Pipeline
	DB         *db.DB
	DBManager  *db.Manager
	Config     *config.Config
	Env        *env.Env
	Log        *log.Log
	View       *view.View
	Cache      *cache.Cache
	Event      *event.Dispatcher
	Lang       *lang.Lang
	Cookie     *cookie.Cookie
	Session    *session.Session
	Debug      *debug.Debug
	Kernel        Kernel
	startupErr    error
	providers     []ServiceProvider // 服务提供者列表
	sessionGCStop func()            // 停止后台会话回收协程
}

// NewApp creates a new App instance
func NewApp(basePath ...string) *App {
	var path string
	if len(basePath) > 0 {
		path = basePath[0]
	} else {
		path, _ = os.Getwd()
	}

	app := &App{
		Container:  NewContainer(),
		BasePath:   path,
		Route:      route.NewRouter(),
		Middleware: middleware.NewPipeline(),
		Config:     config.NewConfig(),
		Env:        env.NewEnv(),
		Log:        log.NewLog(logDriver.NewFile(path + RuntimeLogDir)),
		Event:      event.NewDispatcher(),
		Lang:       lang.NewLang(),
		Debug:      debug.NewDebug(),
	}
	app.Initialize()

	return app
}

// Run 启动应用
// 通过 Kernel 接口解耦 HTTP 服务与框架核心，避免循环依赖
func (app *App) Run() {
	defer func() {
		if app.sessionGCStop != nil {
			app.sessionGCStop()
		}
		if app.DBManager != nil {
			_ = app.DBManager.Close()
		} else if app.DB != nil {
			_ = app.DB.Close()
		}
		if app.Log != nil {
			app.Log.Shutdown()
		}
	}()

	if app.startupErr != nil {
		if app.Log != nil {
			app.Log.Error("Application startup failed: " + app.startupErr.Error())
		}
		panic(app.startupErr)
	}
	// 启动所有服务提供者
	app.BootProviders()

	if app.Kernel != nil {
		if err := app.Kernel.Run(); err != nil {
			if app.Log != nil {
				app.Log.Error("Server error: " + err.Error())
			}
			fmt.Printf("Server error: %v\n", err)
		}
	} else {
		if app.Log != nil {
			app.Log.Error("Http Kernel not initialized.")
		}
		fmt.Println("Http Kernel not initialized.")
	}
}

// RegisterProvider 注册服务提供者
// 对应 ThinkPHP 8 的 $app->register()
func (app *App) RegisterProvider(provider ServiceProvider) {
	app.providers = append(app.providers, provider)
	provider.Register(app)
}

// BootProviders 启动所有已注册的服务提供者
// 在所有 Provider 的 Register 完成后调用各自的 Boot
func (app *App) BootProviders() {
	for _, provider := range app.providers {
		provider.Boot(app)
	}
}

// Kernel interface
type Kernel interface {
	Run() error
}

// IsDebug returns true if debug mode is on
func (app *App) IsDebug() bool {
	return app.DebugMode
}

// Environment returns the current environment
func (app *App) Environment() string {
	env := os.Getenv("APP_ENV")
	if env == "" {
		return "production"
	}
	return env
}

// ==================== URL生成方法 ====================

// Domain 获取服务器域名
func (app *App) Domain() string {
	return app.Env.Get("SERVER_DOMAIN", "https://localhost")
}

// URL 生成完整的URL
// 如果path已经是完整URL（以http://或https://开头），则直接返回
// 否则拼接服务器域名和路径
func (app *App) URL(path string) string {
	if path == "" {
		return ""
	}
	// 如果已经是完整URL，直接返回
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	// 确保path以/开头
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return app.Domain() + path
}

// AssetURL 生成静态资源URL
func (app *App) AssetURL(path string) string {
	return app.URL(path)
}

// Initialize initializes the application
func (app *App) Initialize() {
	// 1. Load .env
	app.Env.Load(app.BasePath + "/.env")

	// 2. Load config files
	if err := app.Config.LoadAll(app.BasePath + "/config"); err != nil && app.startupErr == nil {
		app.startupErr = fmt.Errorf("load config failed: %w", err)
	}

	// 3. Override with Env
	// App Config
	if val := app.Env.Get("APP_DEBUG"); val != "" {
		app.Config.Set("app.app_debug", val == "true")
	}
	if val := app.Env.Get("APP_TRACE"); val != "" {
		app.Config.Set("app.app_trace", val == "true")
	}
	app.DebugMode = app.Config.Get("app.app_debug", false).(bool)
	app.Debug.Enabled = app.Config.Get("app.app_trace", false).(bool)

	// Server Config
	serverConfig := app.Config.Get("app.server", make(map[string]interface{})).(map[string]interface{})
	if val := app.Env.Get("SERVER_HOST"); val != "" {
		serverConfig["host"] = val
	}
	if val := app.Env.Get("SERVER_PORT"); val != "" {
		// 环境变量覆盖端口配置（字符串类型，由 Http.parseConfig 处理类型转换）
		serverConfig["port"] = val
	}
	if val := app.Env.Get("SERVER_HTTP3"); val != "" {
		serverConfig["http3"] = val == "true"
	}

	// TLS Config
	tlsConfig, ok := serverConfig["tls"].(map[string]interface{})
	if !ok {
		tlsConfig = make(map[string]interface{})
		serverConfig["tls"] = tlsConfig
	}
	if val := app.Env.Get("SERVER_TLS_ENABLE"); val != "" {
		tlsConfig["enable"] = val == "true"
	}
	if val := app.Env.Get("SERVER_TLS_CERT"); val != "" {
		tlsConfig["cert_file"] = val
	}
	if val := app.Env.Get("SERVER_TLS_KEY"); val != "" {
		tlsConfig["key_file"] = val
	}
	app.Config.Set("app.server", serverConfig)

	// 3.5 Init Log
	logConfig := app.Config.Get("log", make(map[string]interface{})).(map[string]interface{})
	defaultChannel := "file"
	if v, ok := logConfig["default"].(string); ok {
		defaultChannel = v
	}

	if channels, ok := logConfig["channels"].(map[string]interface{}); ok {
		if channelConfig, ok := channels[defaultChannel].(map[string]interface{}); ok {
			// 驱动类型
			driverType := "file"
			if t, ok := channelConfig["type"].(string); ok {
				driverType = t
			}

			var lDriver log.Driver
			switch driverType {
			case "file":
				path := app.BasePath + RuntimeLogDir
				if p, ok := channelConfig["path"].(string); ok {
					path = p
				}
				// 文件大小轮转限制（字节）
				var maxFileSize int64
				if v, ok := channelConfig["max_file_size"].(float64); ok {
					maxFileSize = int64(v)
				}
				lDriver = logDriver.NewFile(path, maxFileSize)
			default:
				lDriver = logDriver.NewFile(app.BasePath + RuntimeLogDir)
			}

			app.Log = log.NewLog(lDriver)

			// 调试模式下启用控制台彩色输出和调用位置记录
			if app.DebugMode {
				app.Log.AddDriver(logDriver.NewConsole())
				app.Log.SetCallerEnabled(true)
			}

			// 日志级别过滤
			if levels, ok := channelConfig["level"].([]interface{}); ok {
				lvlStrs := make([]string, 0)
				for _, l := range levels {
					if s, ok := l.(string); ok {
						lvlStrs = append(lvlStrs, s)
					}
				}
				app.Log.SetLevels(lvlStrs)
			}
			app.Instance("log", app.Log)
		}
	}
	if channels, ok := logConfig["channels"].(map[string]interface{}); ok && app.Log != nil {
		for name, rawChannelConfig := range channels {
			if name == defaultChannel {
				continue
			}
			channelConfig, ok := rawChannelConfig.(map[string]interface{})
			if !ok {
				continue
			}
			app.Log.RegisterChannel(name, createAppLogChannel(app, channelConfig, false))
		}
	}

	// 4. Init Lang（从 config/lang.json 读取完整多语言配置）
	langConfig := app.Config.Get("lang", make(map[string]interface{})).(map[string]interface{})
	// 兼容旧配置：如果 lang 配置没有 default_lang，从 app 配置读取
	if _, ok := langConfig["default_lang"]; !ok {
		langConfig["default_lang"] = app.Config.Get("app.default_lang", "zh-cn")
	}
	app.Lang.Init(langConfig)
	app.Lang.LoadAll(app.BasePath + "/app/lang")
	// 5. Init Cache
	cacheConfig := app.Config.Get("cache", make(map[string]interface{})).(map[string]interface{})
	defaultStore := "file"
	if v, ok := cacheConfig["default"].(string); ok {
		defaultStore = v
	}

	var cDriver cache.Driver
	if stores, ok := cacheConfig["stores"].(map[string]interface{}); ok {
		if storeConfig, ok := stores[defaultStore].(map[string]interface{}); ok {
			driverType := storeConfig["type"].(string)
			switch driverType {
			case "redis":
				cDriver = cacheDriver.NewRedis(storeConfig)
			case "file":
				path := app.BasePath + RuntimeCacheDir
				if p, ok := storeConfig["path"].(string); ok {
					path = p
				}
				cDriver = cacheDriver.NewFile(path)
			default:
				cDriver = cacheDriver.NewFile(app.BasePath + RuntimeCacheDir)
			}
		}
	}
	if cDriver == nil {
		cDriver = cacheDriver.NewFile(app.BasePath + RuntimeCacheDir)
	}
	app.Cache = cache.NewCache(app.Debug, cDriver)
	if stores, ok := cacheConfig["stores"].(map[string]interface{}); ok {
		for name, rawStoreConfig := range stores {
			if name == defaultStore {
				continue
			}
			storeConfig, ok := rawStoreConfig.(map[string]interface{})
			if !ok {
				continue
			}
			app.Cache.RegisterStore(name, createAppCacheDriver(app, storeConfig))
		}
	}
	app.Instance("cache", app.Cache)

	// 6. Init View
	viewConfig := app.Config.Get("view", make(map[string]interface{})).(map[string]interface{})
	normalizeViewPath(app.BasePath, viewConfig)
	app.View = view.NewView(app.Debug, viewConfig)
	app.View.SetDriver(driver.NewGoTemplate())

	// Register global view functions
	app.View.SetFuncMap(map[string]interface{}{
		"lang": func(key string) string {
			return app.Lang.Get(key, nil, "")
		},
	})

	app.Instance("view", app.View)

	// 7. Init Cookie
	cookieConfig := app.Config.Get("cookie", make(map[string]interface{})).(map[string]interface{})
	app.Cookie = cookie.NewCookie(cookieConfig)

	// 8. Init Session
	sessionConfig := app.Config.Get("session", make(map[string]interface{})).(map[string]interface{})
	sessDriverType := "file"
	if t, ok := sessionConfig["type"].(string); ok {
		sessDriverType = t
	}
	var sessDriver session.Driver
	switch sessDriverType {
	case "file":
		path := app.BasePath + RuntimeSessionDir
		if p, ok := sessionConfig["path"].(string); ok {
			path = p
		}
		sessDriver = sessionDriver.NewFile(path)
	case "memory":
		sessDriver = sessionDriver.NewMemory()
	default:
		sessDriver = sessionDriver.NewFile(app.BasePath + RuntimeSessionDir)
	}
	app.Session = session.NewSession(sessionConfig, sessDriver, app.Cookie)
	app.Session.SetLogger(app.Log)

	// 9. Init Database
	dbConfigData := app.Config.Get("database", make(map[string]interface{})).(map[string]interface{})
	defaultConn := "mysql"
	if v, ok := dbConfigData["default"].(string); ok {
		defaultConn = v
	}
	app.DBManager = db.NewManager(defaultConn)

	if conns, ok := dbConfigData["connections"].(map[string]interface{}); ok && len(conns) > 0 {
		for name, rawConnConfig := range conns {
			connConfig, ok := rawConnConfig.(map[string]interface{})
			if !ok {
				continue
			}

			dbConfig := readDatabaseConfig(connConfig)
			if name == defaultConn {
				applyDatabaseEnvOverrides(app, &dbConfig)
			}
			applyDatabaseFallbacks(&dbConfig)

			database, err := db.Connect(dbConfig)
			if err != nil {
				app.Log.Error("Database connection failed: " + err.Error())
				if app.startupErr == nil {
					app.startupErr = fmt.Errorf("database connection failed: %w", err)
				}
				fmt.Printf("Database connection failed: %v\n", err)
				continue
			}

			database.SetLogger(app.Log)
			app.DBManager.Add(name, database)
			if name == defaultConn || app.DB == nil {
				app.DB = database
			}
		}
	}

	if app.DB == nil {
		dbConfig := db.Config{}
		applyDatabaseEnvOverrides(app, &dbConfig)
		applyDatabaseFallbacks(&dbConfig)

		database, err := db.Connect(dbConfig)
		if err != nil {
			app.Log.Error("Database connection failed: " + err.Error())
			if app.startupErr == nil {
				app.startupErr = fmt.Errorf("database connection failed: %w", err)
			}
			fmt.Printf("Database connection failed: %v\n", err)
		} else {
			database.SetLogger(app.Log)
			app.DB = database
			app.DBManager.Add(defaultConn, database)
		}
	}

	// 10. 注册控制器类型到容器（使用工厂模式，避免并发请求复用同一实例）
	for name, controllerType := range ControllerRegistry {
		app.BindFactory(name, controllerType)
	}

	// 11. Register Global Middleware
	recovery := &middleware.Recovery{
		App:    app,
		Log:    app.Log,
		TplDir: app.BasePath + "/framework/exception/tpl",
	}
	app.Middleware.Pipe(recovery.Handle)

	// Session
	if app.Config.Get("app.session_enable", false).(bool) {
		sessionMiddleware := &middleware.Session{Manager: app.Session}
		app.Middleware.Pipe(sessionMiddleware.Handle)
		// 启动后台会话回收，避免文件型会话在磁盘无限堆积。
		app.sessionGCStop = app.Session.StartGarbageCollector(time.Hour)
	}

	// Trace
	if app.Config.Get("app.app_trace", false).(bool) {
		trace := &middleware.Trace{Debug: app.Debug}
		app.Middleware.Pipe(trace.Handle)
	}

	// Lang
	app.Middleware.Pipe(app.LoadLangPack())

	// User Middleware
	for _, handler := range MiddlewareRegistry {
		app.Middleware.Pipe(handler)
	}

	// 12. Load Routes
	for _, loader := range RouteRegistry {
		loader(app)
	}

	// 触发路由加载完成事件（对应 ThinkPHP 的 RouteLoaded）
	app.Event.Dispatch(event.NewRouteLoadedEvent())

	// 触发应用初始化完成事件（对应 ThinkPHP 的 AppInit）
	app.Event.Dispatch(event.NewAppInitEvent())
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

// applyDatabaseEnvOverrides 用环境变量覆盖数据库配置，避免部署环境必须改配置文件。
func applyDatabaseEnvOverrides(app *App, dbConfig *db.Config) {
	if app == nil || dbConfig == nil {
		return
	}

	if val := app.Env.Get("DB_TYPE"); val != "" {
		dbConfig.Type = val
	}
	if val := app.Env.Get("DB_HOST"); val != "" {
		dbConfig.Hostname = val
	}
	if val := app.Env.Get("DB_PORT"); val != "" {
		dbConfig.Hostport = val
	}
	if val := app.Env.Get("DB_USER"); val != "" {
		dbConfig.Username = val
	}
	if val := app.Env.Get("DB_PASS"); val != "" {
		dbConfig.Password = val
	}
	if val := app.Env.Get("DB_NAME"); val != "" {
		dbConfig.Database = val
	}
	if val := app.Env.Get("DB_MAX_OPEN_CONNS"); val != "" {
		dbConfig.MaxOpenConns = readEnvPositiveInt(val, dbConfig.MaxOpenConns)
	}
	if val := app.Env.Get("DB_MAX_IDLE_CONNS"); val != "" {
		dbConfig.MaxIdleConns = readEnvPositiveInt(val, dbConfig.MaxIdleConns)
	}
	if val := app.Env.Get("DB_CONN_MAX_LIFETIME_SECONDS"); val != "" {
		dbConfig.ConnMaxLifetimeSeconds = readEnvPositiveInt(val, dbConfig.ConnMaxLifetimeSeconds)
	}
	if val := app.Env.Get("DB_CONN_MAX_IDLE_TIME_SECONDS"); val != "" {
		dbConfig.ConnMaxIdleTimeSeconds = readEnvPositiveInt(val, dbConfig.ConnMaxIdleTimeSeconds)
	}
	if val := app.Env.Get("DB_TIMESTAMP_VALUE_TYPE"); val != "" {
		dbConfig.TimestampValueType = val
	}
}

func readDatabaseConfig(connConfig map[string]interface{}) db.Config {
	config := db.Config{}
	config.Type, _ = connConfig["type"].(string)
	config.Hostname, _ = connConfig["hostname"].(string)
	config.Hostport, _ = connConfig["hostport"].(string)
	config.Database, _ = connConfig["database"].(string)
	config.Username, _ = connConfig["username"].(string)
	config.Password, _ = connConfig["password"].(string)
	config.Charset, _ = connConfig["charset"].(string)
	config.Prefix, _ = connConfig["prefix"].(string)
	if debug, ok := connConfig["debug"].(bool); ok {
		config.Debug = debug
	}
	if auto, ok := connConfig["auto_timestamp"].(bool); ok {
		config.AutoTimestamp = auto
	}
	if field, ok := connConfig["create_time_field"].(string); ok {
		config.CreateTimeField = field
	}
	if field, ok := connConfig["update_time_field"].(string); ok {
		config.UpdateTimeField = field
	}
	if valueType, ok := connConfig["timestamp_value_type"].(string); ok {
		config.TimestampValueType = valueType
	}
	config.MaxOpenConns = readConfigIntValue(connConfig["max_open_conns"])
	config.MaxIdleConns = readConfigIntValue(connConfig["max_idle_conns"])
	config.ConnMaxLifetimeSeconds = readConfigIntValue(connConfig["conn_max_lifetime_seconds"])
	config.ConnMaxIdleTimeSeconds = readConfigIntValue(connConfig["conn_max_idle_time_seconds"])
	return config
}

func applyDatabaseFallbacks(dbConfig *db.Config) {
	if dbConfig.Type == "" {
		dbConfig.Type = "mysql"
	}
	if dbConfig.Hostname == "" {
		dbConfig.Hostname = "127.0.0.1"
	}
	if dbConfig.Hostport == "" {
		dbConfig.Hostport = "3306"
	}
	if dbConfig.Username == "" {
		dbConfig.Username = "root"
	}
	if dbConfig.TimestampValueType == "" {
		dbConfig.TimestampValueType = db.TimestampValueTypeUnix
	}
}

func createAppLogChannel(app *App, channelConfig map[string]interface{}, addConsole bool) *log.Log {
	driverType := "file"
	if value, ok := channelConfig["type"].(string); ok {
		driverType = value
	}

	var driverInstance log.Driver
	switch driverType {
	case "file":
		path := app.BasePath + RuntimeLogDir
		if value, ok := channelConfig["path"].(string); ok {
			path = value
		}
		var maxFileSize int64
		if value, ok := channelConfig["max_file_size"].(float64); ok {
			maxFileSize = int64(value)
		} else if value, ok := channelConfig["max_file_size"].(int); ok {
			maxFileSize = int64(value)
		}
		driverInstance = logDriver.NewFile(path, maxFileSize)
	default:
		driverInstance = logDriver.NewFile(app.BasePath + RuntimeLogDir)
	}

	logger := log.NewLog(driverInstance)
	if addConsole {
		logger.AddDriver(logDriver.NewConsole())
		logger.SetCallerEnabled(true)
	}
	if levels, ok := channelConfig["level"].([]interface{}); ok {
		levelNames := make([]string, 0, len(levels))
		for _, level := range levels {
			if name, ok := level.(string); ok {
				levelNames = append(levelNames, name)
			}
		}
		logger.SetLevels(levelNames)
	}
	return logger
}

func createAppCacheDriver(app *App, storeConfig map[string]interface{}) cache.Driver {
	driverType, _ := storeConfig["type"].(string)
	switch driverType {
	case "redis":
		return cacheDriver.NewRedis(storeConfig)
	case "file":
		path := app.BasePath + RuntimeCacheDir
		if value, ok := storeConfig["path"].(string); ok {
			path = value
		}
		return cacheDriver.NewFile(path)
	default:
		return cacheDriver.NewFile(app.BasePath + RuntimeCacheDir)
	}
}

// readConfigIntValue 读取 JSON 配置中的整数值，非法类型返回 0。
func readConfigIntValue(raw interface{}) int {
	switch typed := raw.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		return readEnvPositiveInt(typed, 0)
	default:
		return 0
	}
}

// readEnvPositiveInt 读取正整数环境变量，非法值回退到默认值。
func readEnvPositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
