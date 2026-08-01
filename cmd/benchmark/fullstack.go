package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	goruntime "runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"

	"thinkgo/framework"
	"thinkgo/framework/cache"
	cacheDriver "thinkgo/framework/cache/driver"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/cookie"
	"thinkgo/framework/db"
	"thinkgo/framework/env"
	fwhttp "thinkgo/framework/http"
	"thinkgo/framework/log"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
	"thinkgo/framework/session"
	sessionDriver "thinkgo/framework/session/driver"
)

var fullBenchmarkValidationRules = map[string]string{
	"email":    "required|email",
	"name":     "required|length:2,32",
	"quantity": "required|integer|between:1,100",
}

// fullBenchmarkRuntime 保存全特性基准所需的多应用宿主和数据库资源。
type fullBenchmarkRuntime struct {
	manager *framework.ApplicationManager
	app     *framework.App
	host    *fwhttp.MultiHttp
}

// benchmarkDBConfig 统一生成基准数据库配置，避免不同基准入口使用不同连接池参数。
func benchmarkDBConfig(app *framework.App) db.Config {
	return db.Config{
		Type:                   "mysql",
		Hostname:               envOr(app, "DB_HOST", "127.0.0.1"),
		Hostport:               envOr(app, "DB_PORT", "3306"),
		Username:               envOr(app, "DB_USER", "root"),
		Password:               envOr(app, "DB_PASS", ""),
		Database:               envOr(app, "DB_NAME", "thinkgo_benchmark"),
		Charset:                "utf8mb4",
		AutoTimestamp:          true,
		CreateTimeField:        "create_time",
		UpdateTimeField:        "update_time",
		TimestampValueType:     envOr(app, "DB_TIMESTAMP_VALUE_TYPE", "unix"),
		MaxOpenConns:           envInt(app, "DB_MAX_OPEN_CONNS", 512),
		MaxIdleConns:           envInt(app, "DB_MAX_IDLE_CONNS", 128),
		ConnMaxLifetimeSeconds: envInt(app, "DB_CONN_MAX_LIFETIME_SECONDS", 300),
		ConnMaxIdleTimeSeconds: envInt(app, "DB_CONN_MAX_IDLE_TIME_SECONDS", 60),
		Params:                 benchmarkDBParams(app),
	}
}

func connectBenchmarkDatabase(app *framework.App) (*db.DB, error) {
	if _, err := framework.ResolveServiceAs[*env.Env](app, framework.ServiceEnv); err != nil {
		return nil, errors.New("benchmark application environment is unavailable")
	}
	database, err := db.Connect(benchmarkDBConfig(app))
	if err != nil {
		return nil, err
	}
	logger, err := framework.ResolveServiceAs[*log.Log](app, framework.ServiceLog)
	if err != nil {
		return nil, fmt.Errorf("benchmark application log is unavailable: %w", err)
	}
	database.SetLogger(logger)
	return database, nil
}

// newFullBenchmarkRuntime 使用 ApplicationManager 创建真实应用生命周期，再挂载基准数据库。
func newFullBenchmarkRuntime() (*fullBenchmarkRuntime, error) {
	manager, err := framework.NewApplicationManagerFromDefinitions(".", []framework.ApplicationDefinition{
		{
			Name: "index",
			Path: "app/index",
			Register: func(app *framework.App) error {
				return registerFullBenchmarkApplication(app)
			},
		},
	}, true)
	if err != nil {
		return nil, fmt.Errorf("create full benchmark application: %w", err)
	}
	app := manager.DefaultApplication()
	if app == nil {
		_ = manager.Close()
		return nil, errors.New("full benchmark application is unavailable")
	}
	database, err := connectBenchmarkDatabase(app)
	if err != nil {
		_ = manager.Close()
		return nil, fmt.Errorf("connect full benchmark database: %w", err)
	}
	managerService, err := framework.ResolveServiceAs[*db.Manager](app, framework.ServiceDBManager)
	if err != nil {
		_ = database.Close()
		_ = manager.Close()
		return nil, fmt.Errorf("resolve full benchmark database manager: %w", err)
	}
	if err = managerService.Add("default", database); err != nil {
		_ = database.Close()
		_ = manager.Close()
		return nil, fmt.Errorf("register full benchmark database: %w", err)
	}
	app.Instance(string(framework.ServiceDB), database)
	if err = configureFullBenchmarkCache(app); err != nil {
		_ = manager.Close()
		return nil, err
	}
	if err = configureFullBenchmarkSession(app); err != nil {
		_ = manager.Close()
		return nil, err
	}
	host, err := fwhttp.NewMultiHttp(manager)
	if err != nil {
		_ = manager.Close()
		return nil, fmt.Errorf("create full benchmark HTTP host: %w", err)
	}
	return &fullBenchmarkRuntime{manager: manager, app: app, host: host}, nil
}

func (r *fullBenchmarkRuntime) Close() error {
	if r == nil || r.manager == nil {
		return nil
	}
	return r.manager.Close()
}

// serveFull 使用统一多应用 HTTP 宿主，覆盖应用解析、控制器分发和完整中间件链路。
type fullProfileOptions struct {
	cpuPath   string
	heapPath  string
	mutexPath string
	blockPath string
	duration  time.Duration
}

// fullProfileCapture 管理全特性基准的剖析生命周期，确保服务退出时仍能落盘已采集的数据。
type fullProfileCapture struct {
	stop func()
}

const (
	fullMutexProfileFraction = 5
	fullBlockProfileRate     = int(time.Millisecond)
)

func serveFull(runtime *fullBenchmarkRuntime, addr string, accessLog bool, cpuProfilePath, heapProfilePath, mutexProfilePath, blockProfilePath string, cpuProfileDuration time.Duration) error {
	if runtime == nil || runtime.manager == nil || runtime.host == nil || runtime.app == nil {
		return errors.New("full benchmark runtime is unavailable")
	}
	if _, err := framework.ResolveServiceAs[*db.DB](runtime.app, framework.ServiceDB); err != nil {
		return errors.New("full benchmark database is unavailable")
	}
	if err := runtime.manager.Boot(); err != nil {
		return fmt.Errorf("boot full benchmark application: %w", err)
	}
	if !accessLog {
		if logger, err := framework.ResolveServiceAs[*log.Log](runtime.app, framework.ServiceLog); err == nil && logger != nil {
			logger.SetLevels([]string{"warning", "error"})
		}
	}
	profiles, err := startFullProfileCapture(fullProfileOptions{
		cpuPath:   cpuProfilePath,
		heapPath:  heapProfilePath,
		mutexPath: mutexProfilePath,
		blockPath: blockProfilePath,
		duration:  cpuProfileDuration,
	})
	if err != nil {
		return err
	}
	if profiles != nil {
		defer profiles.stop()
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           runtime.host,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		<-stop
		_ = server.Shutdown(context.Background())
	}()
	fmt.Printf("full benchmark server listening on http://%s\n", addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func startFullProfileCapture(options fullProfileOptions) (*fullProfileCapture, error) {
	if options.cpuPath == "" && options.heapPath == "" && options.mutexPath == "" && options.blockPath == "" {
		return nil, nil
	}
	if options.duration <= 0 {
		return nil, errors.New("full benchmark profile duration must be positive")
	}

	var cpuFile *os.File
	var err error
	if options.cpuPath != "" {
		cpuFile, err = os.Create(options.cpuPath)
		if err != nil {
			return nil, fmt.Errorf("create full benchmark CPU profile: %w", err)
		}
		if err = pprof.StartCPUProfile(cpuFile); err != nil {
			_ = cpuFile.Close()
			return nil, fmt.Errorf("start full benchmark CPU profile: %w", err)
		}
	}
	previousMutexFraction := 0
	if options.mutexPath != "" {
		previousMutexFraction = goruntime.SetMutexProfileFraction(fullMutexProfileFraction)
	}
	if options.blockPath != "" {
		goruntime.SetBlockProfileRate(fullBlockProfileRate)
	}

	stopSignal := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopSignal)
			<-done
		})
	}
	go func() {
		defer close(done)
		timer := time.NewTimer(options.duration)
		select {
		case <-timer.C:
		case <-stopSignal:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if cpuFile != nil {
			pprof.StopCPUProfile()
			_ = cpuFile.Close()
		}
		if options.heapPath != "" {
			writeFullProfile(options.heapPath, "heap")
		}
		if options.mutexPath != "" {
			writeFullProfile(options.mutexPath, "mutex")
			goruntime.SetMutexProfileFraction(previousMutexFraction)
		}
		if options.blockPath != "" {
			writeFullProfile(options.blockPath, "block")
			goruntime.SetBlockProfileRate(0)
		}
	}()
	return &fullProfileCapture{stop: stop}, nil
}

func writeFullProfile(path, name string) {
	// #nosec G304 -- path 是显式的本地压测 profile 输出路径，不来自服务请求。
	file, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create full benchmark %s profile: %v\n", name, err)
		return
	}
	profile := pprof.Lookup(name)
	if profile == nil {
		fmt.Fprintf(os.Stderr, "full benchmark %s profile is unavailable\n", name)
		_ = file.Close()
		return
	}
	if err = profile.WriteTo(file, 0); err != nil {
		fmt.Fprintf(os.Stderr, "write full benchmark %s profile: %v\n", name, err)
	}
	_ = file.Close()
}

// configureFullBenchmarkCache 允许在同一业务模型下对比默认文件缓存和显式内存缓存。
func configureFullBenchmarkCache(app *framework.App) error {
	driverName := strings.ToLower(strings.TrimSpace(envOr(app, "BENCHMARK_CACHE_DRIVER", "file")))
	if driverName == "" || driverName == "file" {
		return nil
	}
	if driverName != "memory" {
		return fmt.Errorf("unsupported full benchmark cache driver %q", driverName)
	}
	if currentCache, err := framework.ResolveServiceAs[*cache.Cache](app, framework.ServiceCache); err == nil && currentCache != nil {
		_ = currentCache.Close()
	}
	app.Instance(string(framework.ServiceCache), cache.NewCache(nil, cacheDriver.NewMemory()))
	return nil
}

// configureFullBenchmarkSession 允许基准显式切换内存 Session，以区分持久化 I/O 和请求链路本身的成本。
func configureFullBenchmarkSession(app *framework.App) error {
	driverName := strings.ToLower(strings.TrimSpace(envOr(app, "BENCHMARK_SESSION_DRIVER", "file")))
	if driverName == "" || driverName == "file" {
		return nil
	}
	if driverName != "memory" {
		return fmt.Errorf("unsupported full benchmark session driver %q", driverName)
	}
	sessionManager, sessionErr := framework.ResolveServiceAs[*session.Session](app, framework.ServiceSession)
	cookieFactory, cookieErr := framework.ResolveServiceAs[*cookie.Cookie](app, framework.ServiceCookie)
	if sessionErr != nil || cookieErr != nil || sessionManager == nil || cookieFactory == nil {
		return errors.New("full benchmark session dependencies are unavailable")
	}
	config := sessionManager.GetConfig()
	config.DriverType = "memory"
	manager, err := session.NewSessionWithConfig(config, sessionDriver.NewMemory(), cookieFactory)
	if err != nil {
		return fmt.Errorf("create full benchmark memory session: %w", err)
	}
	logger, err := framework.ResolveServiceAs[*log.Log](app, framework.ServiceLog)
	if err != nil {
		return fmt.Errorf("resolve full benchmark log: %w", err)
	}
	manager.SetLogger(logger)
	app.Instance(string(framework.ServiceSession), manager)
	return nil
}

func registerFullBenchmarkApplication(app *framework.App) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	if err := app.RegisterController("Benchmark", &fullBenchmarkController{}); err != nil {
		return fmt.Errorf("register full benchmark controller: %w", err)
	}
	if err := app.RegisterGlobalMiddleware(fullBenchmarkMiddleware); err != nil {
		return fmt.Errorf("register full benchmark middleware: %w", err)
	}
	sessionManager, err := framework.ResolveServiceAs[*session.Session](app, framework.ServiceSession)
	if err != nil {
		return fmt.Errorf("resolve full benchmark session: %w", err)
	}
	if err := app.RegisterGlobalMiddleware(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return (&middleware.Session{Manager: sessionManager}).Handle(req, next)
	}); err != nil {
		return fmt.Errorf("register full benchmark session middleware: %w", err)
	}
	pipeline, err := framework.ResolveServiceAs[*middleware.Pipeline](app, framework.ServiceMiddleware)
	if err != nil {
		return fmt.Errorf("resolve full benchmark middleware: %w", err)
	}
	pipeline.Alias("full-benchmark-controller", fullBenchmarkControllerMiddleware)
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		return fmt.Errorf("resolve full benchmark route: %w", err)
	}
	for _, registration := range []struct {
		path    string
		handler string
	}{
		{path: "/bench/full/business", handler: "Benchmark@Business"},
		{path: "/bench/full/cache", handler: "Benchmark@Cache"},
	} {
		if _, err := router.Post(registration.path, registration.handler); err != nil {
			return err
		}
		if _, err := router.Get(registration.path, registration.handler); err != nil {
			return err
		}
	}
	return nil
}

// fullBenchmarkMiddleware 模拟应用级请求链路，记录请求标记并透传业务响应。
func fullBenchmarkMiddleware(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
	if req != nil {
		req.Set("full_benchmark_global_middleware", true)
	}
	response := next(req)
	if response != nil {
		response.Header("X-Full-Benchmark-Middleware", "global")
	}
	return response
}

// fullBenchmarkControllerMiddleware 模拟控制器级别中间件，不改变业务返回值。
func fullBenchmarkControllerMiddleware(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
	if req != nil {
		req.Set("full_benchmark_controller_middleware", true)
	}
	return next(req)
}

type fullBenchmarkController struct {
	framework.Controller
}

// Init 使用框架控制器初始化和控制器级中间件声明。
func (c *fullBenchmarkController) Init(app *framework.App, req *fwcontext.Request) {
	c.Controller.Init(app, req)
	c.SetMiddleware(framework.ControllerMiddleware{Name: "full-benchmark-controller"})
}

// Business 覆盖控制器、验证器、多语言、缓存、Session 和 ORM 事务前的读链路。
func (c *fullBenchmarkController) Business(req *fwcontext.Request) *fwcontext.Response {
	if c == nil || c.App == nil || req == nil {
		return fullBenchmarkError(c, "full benchmark dependencies unavailable", http.StatusInternalServerError)
	}
	database, err := framework.ResolveServiceAs[*db.DB](c.App, framework.ServiceDB)
	if err != nil || database == nil {
		return fullBenchmarkError(c, "full benchmark dependencies unavailable", http.StatusInternalServerError)
	}
	requestCache := c.RequestCache()
	if requestCache == nil {
		return c.Error("full benchmark cache unavailable", http.StatusInternalServerError)
	}
	input := map[string]interface{}{
		"email":    requestValue(req, "email", "bench@example.com"),
		"name":     requestValue(req, "name", "Ada"),
		"quantity": requestValue(req, "quantity", "1"),
	}
	if req.Get("invalid", "") == "1" {
		input["email"] = "invalid-email"
	}
	result, err := c.Validate(input, fullBenchmarkValidationRules)
	if err != nil {
		return c.Error("full benchmark validation configuration failed", http.StatusInternalServerError)
	}
	if !result.Valid() {
		return c.Success(map[string]interface{}{
			"valid":   false,
			"message": c.Lang("validate.email_format"),
		}, c.Lang("controller.get_success"))
	}

	productID := boundedInt(requestValue(req, "product_id", "1"), 1, productCount, 1)
	cacheKey := "bench-full-product-" + strconvItoa(productID)
	if requestedKey := req.Get("cache_key", ""); requestedKey != "" {
		cacheKey = requestedKey
	}
	value, err := requestCache.Remember(cacheKey, time.Minute, func() (interface{}, error) {
		return database.Table(benchmarkTable("products")).WithContext(req.Raw().Context()).WhereField("id", "=", productID).Field("id,sku,name,price,stock").Find()
	})
	if err != nil {
		return c.Error("full benchmark cache/database failed", http.StatusInternalServerError)
	}

	sessionHit := false
	if requestSession, ok := req.GetData("_session").(*session.Session); ok && requestSession != nil {
		if err = requestSession.Set("last_product", productID); err == nil {
			_, sessionHit = requestSession.Get("last_product")
		}
	}
	return c.Success(map[string]interface{}{
		"valid":         true,
		"product":       value,
		"language":      c.Lang("controller.get_success"),
		"session_hit":   sessionHit,
		"global_mw":     req.GetData("full_benchmark_global_middleware") == true,
		"controller_mw": req.GetData("full_benchmark_controller_middleware") == true,
	}, c.Lang("controller.get_success"))
}

// Cache 覆盖 Remember、标签缓存和多语言响应，不依赖数据库查询。
func (c *fullBenchmarkController) Cache(req *fwcontext.Request) *fwcontext.Response {
	if c == nil || req == nil {
		return fullBenchmarkError(c, "full benchmark cache unavailable", http.StatusInternalServerError)
	}
	requestCache := c.RequestCache()
	if requestCache == nil {
		return c.Error("full benchmark cache unavailable", http.StatusInternalServerError)
	}
	key := req.Get("cache_key", "bench-full-cache")
	value, err := requestCache.Remember(key, time.Minute, func() (interface{}, error) {
		return map[string]interface{}{"source": "cache", "language": c.GetLang()}, nil
	})
	if err != nil {
		return c.Error("full benchmark cache remember failed", http.StatusInternalServerError)
	}
	tagged, err := requestCache.Tag("full-benchmark")
	if err != nil {
		return c.Error("full benchmark cache tag failed", http.StatusInternalServerError)
	}
	if err = tagged.Set("full-benchmark-tagged", value, time.Minute); err != nil {
		return c.Error("full benchmark tagged cache failed", http.StatusInternalServerError)
	}
	return c.Success(map[string]interface{}{
		"value":    value,
		"tagged":   true,
		"language": c.Lang("controller.get_success"),
	}, c.Lang("controller.get_success"))
}

func fullBenchmarkError(c *fullBenchmarkController, message string, code int) *fwcontext.Response {
	if c != nil {
		return c.Error(message, code)
	}
	return fwcontext.NewResponse().Code(code).Json(map[string]string{"error": message})
}

func requestValue(req *fwcontext.Request, key, fallback string) string {
	if value := req.Post(key, ""); value != "" {
		return value
	}
	return req.Get(key, fallback)
}

func strconvItoa(value int) string {
	if value < 0 {
		return "0"
	}
	return fmt.Sprintf("%d", value)
}
