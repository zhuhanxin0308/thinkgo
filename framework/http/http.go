package http

import (
	stdcontext "context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go/http3"

	"thinkgo/framework"
	"thinkgo/framework/context"
	"thinkgo/framework/event"
	"thinkgo/framework/exception"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
)

// serverConf 保存启动阶段预解析后的 HTTP 服务配置。
type serverConf struct {
	Host              string
	Port              int
	EnableTLS         bool
	CertFile          string
	KeyFile           string
	EnableHTTP3       bool
	TrustedProxies    []string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	MultipartMemory   int64
}

// compressionConf 保存响应压缩配置，避免每次请求重复读取配置树。
type compressionConf struct {
	Enable  bool
	MinSize int
	Levels  map[string]int
}

// Http 是框架的 HTTP 内核。
type Http struct {
	app            *framework.App
	srvConf        serverConf
	compressConf   compressionConf
	dispatchPlanMu sync.RWMutex
	dispatchPlans  map[string]*controllerDispatchPlan
	spaIndexMu     sync.RWMutex
	spaIndexCache  []byte
	spaIndexMod    time.Time
}

// controllerDispatchPlan 缓存控制器动作的反射计划，避免每次请求重复查找方法。
type controllerDispatchPlan struct {
	controllerType reflect.Type
	actionMethod   reflect.Method
	initMethod     *reflect.Method
}

// NewHttp 创建 HTTP 内核，并在启动时完成一次配置解析。
func NewHttp(app *framework.App) *Http {
	h := &Http{
		app:           app,
		dispatchPlans: make(map[string]*controllerDispatchPlan),
	}
	h.parseConfig()
	return h
}

// parseConfig 解析服务端与压缩配置，避免运行期重复做类型转换。
func (h *Http) parseConfig() {
	serverConfig, _ := h.app.Config.Get("app.server", make(map[string]interface{})).(map[string]interface{})
	h.srvConf = serverConf{
		Host:              "0.0.0.0",
		Port:              8080,
		CertFile:          "./runtime/cert.pem",
		KeyFile:           "./runtime/key.pem",
		TrustedProxies:    []string{},
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   5 * time.Second,
		MaxHeaderBytes:    1 << 20,
		MaxBodyBytes:      10 << 20,
		MultipartMemory:   32 << 20,
	}

	if value, ok := serverConfig["host"].(string); ok {
		h.srvConf.Host = value
	}
	h.srvConf.Port = parseInt(serverConfig["port"], h.srvConf.Port)

	tlsConfig, _ := serverConfig["tls"].(map[string]interface{})
	if value, ok := tlsConfig["enable"].(bool); ok {
		h.srvConf.EnableTLS = value
	}
	if value, ok := tlsConfig["cert_file"].(string); ok {
		h.srvConf.CertFile = value
	}
	if value, ok := tlsConfig["key_file"].(string); ok {
		h.srvConf.KeyFile = value
	}
	if value, ok := serverConfig["http3"].(bool); ok {
		h.srvConf.EnableHTTP3 = value
	}

	h.srvConf.ReadHeaderTimeout = parseMilliseconds(serverConfig["read_header_timeout_ms"], h.srvConf.ReadHeaderTimeout)
	h.srvConf.ReadTimeout = parseMilliseconds(serverConfig["read_timeout_ms"], h.srvConf.ReadTimeout)
	h.srvConf.WriteTimeout = parseMilliseconds(serverConfig["write_timeout_ms"], h.srvConf.WriteTimeout)
	h.srvConf.IdleTimeout = parseMilliseconds(serverConfig["idle_timeout_ms"], h.srvConf.IdleTimeout)
	h.srvConf.ShutdownTimeout = parseMilliseconds(serverConfig["shutdown_timeout_ms"], h.srvConf.ShutdownTimeout)
	h.srvConf.MaxHeaderBytes = parseInt(serverConfig["max_header_bytes"], h.srvConf.MaxHeaderBytes)
	h.srvConf.MaxBodyBytes = parseInt64(serverConfig["max_body_bytes"], h.srvConf.MaxBodyBytes)

	multipartMemoryMB := parseInt64(serverConfig["multipart_max_memory_mb"], h.srvConf.MultipartMemory>>20)
	if multipartMemoryMB > 0 {
		h.srvConf.MultipartMemory = multipartMemoryMB << 20
	}

	if values, ok := serverConfig["trusted_proxies"].([]interface{}); ok {
		for _, value := range values {
			if proxy, ok := value.(string); ok && strings.TrimSpace(proxy) != "" {
				h.srvConf.TrustedProxies = append(h.srvConf.TrustedProxies, strings.TrimSpace(proxy))
			}
		}
	} else if values, ok := serverConfig["trusted_proxies"].([]string); ok {
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				h.srvConf.TrustedProxies = append(h.srvConf.TrustedProxies, strings.TrimSpace(value))
			}
		}
	} else if value, ok := serverConfig["trusted_proxies"].(string); ok && strings.TrimSpace(value) != "" {
		for _, proxy := range strings.Split(value, ",") {
			if strings.TrimSpace(proxy) != "" {
				h.srvConf.TrustedProxies = append(h.srvConf.TrustedProxies, strings.TrimSpace(proxy))
			}
		}
	}

	if h.app.Env != nil {
		if value := strings.TrimSpace(h.app.Env.Get("SERVER_TRUSTED_PROXIES", "")); value != "" {
			h.srvConf.TrustedProxies = h.srvConf.TrustedProxies[:0]
			for _, proxy := range strings.Split(value, ",") {
				if strings.TrimSpace(proxy) != "" {
					h.srvConf.TrustedProxies = append(h.srvConf.TrustedProxies, strings.TrimSpace(proxy))
				}
			}
		}
	}

	compressConfig, _ := h.app.Config.Get("app.compression", make(map[string]interface{})).(map[string]interface{})
	h.compressConf = compressionConf{
		MinSize: 1024,
		Levels:  make(map[string]int),
	}
	if enable, ok := compressConfig["enable"].(bool); ok {
		h.compressConf.Enable = enable
	}
	if value, ok := compressConfig["min_size"].(float64); ok {
		h.compressConf.MinSize = int(value)
	} else if value, ok := compressConfig["min_size"].(int); ok {
		h.compressConf.MinSize = value
	}

	if levels, ok := compressConfig["levels"].(map[string]interface{}); ok {
		for key, value := range levels {
			if level, ok := value.(float64); ok {
				h.compressConf.Levels[key] = int(level)
			} else if level, ok := value.(int); ok {
				h.compressConf.Levels[key] = level
			}
		}
	} else if level, ok := compressConfig["level"].(float64); ok {
		lvl := int(level)
		h.compressConf.Levels["gzip"] = lvl
		h.compressConf.Levels["deflate"] = lvl
		h.compressConf.Levels["br"] = lvl
		h.compressConf.Levels["zstd"] = 2
	}
}

// Run 启动 HTTP 服务。
func (h *Http) Run() error {
	addr := fmt.Sprintf("%s:%d", h.srvConf.Host, h.srvConf.Port)
	server := h.newServer()
	stopShutdown := h.listenForShutdown(server)
	defer stopShutdown()

	fmt.Println("ThinkGo starting on " + addr)

	if h.srvConf.EnableTLS {
		if h.srvConf.EnableHTTP3 {
			fmt.Println("HTTP/3 Enabled")
			go func() {
				http3Server := http3.Server{
					Addr:    addr,
					Handler: h,
				}
				_ = http3Server.ListenAndServeTLS(h.srvConf.CertFile, h.srvConf.KeyFile)
			}()
		}

		err := server.ListenAndServeTLS(h.srvConf.CertFile, h.srvConf.KeyFile)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}

	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeHTTP 统一处理静态文件、动态路由、异常恢复和访问日志。
func (h *Http) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.srvConf.EnableHTTP3 {
		w.Header().Set("Alt-Svc", fmt.Sprintf(`h3=":%d"; ma=2592000`, h.srvConf.Port))
	}

	statusWriter := newStatusTrackingResponseWriter(w)
	w = statusWriter

	req := context.NewRequest(
		r,
		context.WithTrustedProxies(h.srvConf.TrustedProxies),
		context.WithMultipartMemoryLimit(h.srvConf.MultipartMemory),
	)
	start := time.Now()
	var compressionWriter *CompressionResponseWriter

	defer func() {
		if recovered := recover(); recovered != nil {
			handler := &exception.Handle{
				App:    h.app,
				Log:    h.app.Log,
				TplDir: filepath.Join(h.app.BasePath, "framework", "exception", "tpl"),
			}
			handler.Render(w, r, recovered)
		}

		if compressionWriter != nil {
			_ = compressionWriter.Close()
		}

		duration := time.Since(start)
		finalStatus := statusWriter.Status()
		if h.app != nil && h.app.Log != nil {
			h.app.Log.InfoCtx(
				fmt.Sprintf("%s %s %d %.3fms", r.Method, r.URL.Path, finalStatus, float64(duration.Microseconds())/1000),
				map[string]interface{}{
					"method":      r.Method,
					"path":        r.URL.Path,
					"status":      finalStatus,
					"duration_ms": float64(duration.Microseconds()) / 1000,
					"ip":          req.Ip(),
					"user_agent":  r.UserAgent(),
				},
			)
		}

		// 触发 HTTP 请求结束事件（对应 ThinkPHP 的 HttpEnd）
		if h.app.Event != nil {
			h.app.Event.Dispatch(event.NewHttpEndEvent(statusWriter.Status()))
		}

		req.Cleanup()
	}()

	// 触发 HTTP 请求开始事件（对应 ThinkPHP 的 HttpRun）
	if h.app.Event != nil {
		h.app.Event.Dispatch(event.NewHttpRunEvent())
	}

	if h.compressConf.Enable {
		compressionWriter = NewCompressionResponseWriter(w, r, h.compressConf.MinSize, h.compressConf.Levels)
		w = compressionWriter
	}

	if h.srvConf.MaxBodyBytes > 0 && r.Body != nil {
		if r.ContentLength > h.srvConf.MaxBodyBytes {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, h.srvConf.MaxBodyBytes)
	}

	urlPath := r.URL.Path
	publicDir := filepath.Join(h.app.BasePath, "public")

	if urlPath == "/" {
		indexFile := filepath.Join(publicDir, "index.html")
		if _, err := os.Stat(indexFile); err == nil {
			http.ServeFile(w, r, indexFile)
			return
		}
	}

	if staticFile, ok := h.resolveStaticFilePath(urlPath); ok {
		if info, err := os.Stat(staticFile); err == nil && !info.IsDir() {
			http.ServeFile(w, r, staticFile)
			return
		}
	}

	pipeline := h.app.Middleware
	resp := pipeline.Then(req, func(req *context.Request) *context.Response {
		matchedRoute, routeParams := h.app.Route.Match(req)
		for key, value := range routeParams {
			req.Set(key, value)
		}

		if matchedRoute == nil {
			reqPath := req.Path()
			if !strings.HasPrefix(reqPath, "/api/") {
				if content, ok := h.spaIndexContent(); ok {
					return context.NewResponse().
						Header("Content-Type", "text/html; charset=utf-8").
						Content(string(content))
				}
			}
			return context.NewResponse().Code(http.StatusNotFound).Content("404 Not Found")
		}

		if len(matchedRoute.Middleware) > 0 {
			routePipeline := middleware.NewPipeline()
			for _, handler := range matchedRoute.Middleware {
				routePipeline.Pipe(handler)
			}
			return routePipeline.Then(req, func(req *context.Request) *context.Response {
				return h.dispatch(matchedRoute, req)
			})
		}

		return h.dispatch(matchedRoute, req)
	})

	if resp == nil {
		resp = context.NewResponse().
			Code(http.StatusInternalServerError).
			Content(http.StatusText(http.StatusInternalServerError))
	}
	resp.Send(w)
	h.runTerminators(req, resp, middleware.RequestTerminators(req))
}

// spaIndexContent 返回 SPA 入口 index.html 内容，按文件修改时间缓存，
// 避免每个未命中路由都全量读取磁盘文件；文件更新后会自动失效重载。
func (h *Http) spaIndexContent() ([]byte, bool) {
	indexFile := filepath.Join(h.app.BasePath, "public", "index.html")
	info, err := os.Stat(indexFile)
	if err != nil || info.IsDir() {
		return nil, false
	}
	modTime := info.ModTime()

	h.spaIndexMu.RLock()
	if h.spaIndexCache != nil && h.spaIndexMod.Equal(modTime) {
		content := h.spaIndexCache
		h.spaIndexMu.RUnlock()
		return content, true
	}
	h.spaIndexMu.RUnlock()

	content, err := os.ReadFile(indexFile)
	if err != nil {
		return nil, false
	}

	h.spaIndexMu.Lock()
	h.spaIndexCache = content
	h.spaIndexMod = modTime
	h.spaIndexMu.Unlock()
	return content, true
}

// newServer 基于解析后的配置构建显式 http.Server。
func (h *Http) newServer() *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf("%s:%d", h.srvConf.Host, h.srvConf.Port),
		Handler:           h,
		ReadHeaderTimeout: h.srvConf.ReadHeaderTimeout,
		ReadTimeout:       h.srvConf.ReadTimeout,
		WriteTimeout:      h.srvConf.WriteTimeout,
		IdleTimeout:       h.srvConf.IdleTimeout,
		MaxHeaderBytes:    h.srvConf.MaxHeaderBytes,
	}
}

// listenForShutdown 在收到中断信号后优雅关闭 HTTP Server。
func (h *Http) listenForShutdown(server *http.Server) func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt)

	go func() {
		select {
		case <-signals:
			timeout := h.srvConf.ShutdownTimeout
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			shutdownCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), timeout)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()

	return func() {
		close(done)
		signal.Stop(signals)
	}
}

// resolveStaticFilePath 将 URL 路径安全映射到 public 目录内的绝对路径。
func (h *Http) resolveStaticFilePath(urlPath string) (string, bool) {
	normalizedPath := strings.ReplaceAll(urlPath, "\\", "/")
	cleanPath := path.Clean("/" + normalizedPath)
	relativePath := strings.TrimPrefix(cleanPath, "/")

	publicDir := filepath.Join(h.app.BasePath, "public")
	targetPath := filepath.Join(publicDir, filepath.FromSlash(relativePath))

	publicAbs, err := filepath.Abs(publicDir)
	if err != nil {
		return "", false
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return "", false
	}

	if targetAbs != publicAbs && !strings.HasPrefix(targetAbs, publicAbs+string(os.PathSeparator)) {
		return "", false
	}

	return targetAbs, true
}

// dispatch 将路由处理器分发到函数或控制器方法。
func (h *Http) dispatch(matchedRoute *route.Route, req *context.Request) *context.Response {
	handler := matchedRoute.Handler

	if fn, ok := handler.(func(*context.Request) *context.Response); ok {
		return fn(req)
	}

	if handlerStr, ok := handler.(string); ok {
		parts := strings.Split(handlerStr, "@")
		if len(parts) != 2 {
			return h.dispatchInternalError(handlerStr, fmt.Errorf("invalid route handler format"))
		}

		// 自动路由的动作名来自 URL，禁止其调用控制器基类的内置方法
		// （如 View/Success/Init/SetMiddleware 等），避免把内部能力暴露成可路由端点。
		if matchedRoute.Auto && isReservedControllerMethod(parts[1]) {
			return context.NewResponse().Code(http.StatusNotFound).Content("404 Not Found")
		}

		controllerName := parts[0]
		controllerInstance, err := h.app.Make(controllerName)
		if err != nil {
			return h.dispatchInternalError(handlerStr, err)
		}

		controllerValue := reflect.ValueOf(controllerInstance)
		if controllerValue.Kind() == reflect.Ptr && controllerValue.IsNil() {
			return h.dispatchInternalError(handlerStr, fmt.Errorf("controller instance is nil"))
		}

		plan, err := h.resolveDispatchPlan(handlerStr, controllerValue.Type())
		if err != nil {
			return h.dispatchInternalError(handlerStr, err)
		}

		if plan.initMethod != nil {
			plan.initMethod.Func.Call([]reflect.Value{controllerValue, reflect.ValueOf(h.app), reflect.ValueOf(req)})
		}

		// 实际执行控制器动作的闭包。
		invokeAction := func(req *context.Request) *context.Response {
			args := []reflect.Value{controllerValue}
			if plan.actionMethod.Type.NumIn() > 1 && plan.actionMethod.Type.In(1) == reflect.TypeOf(req) {
				args = append(args, reflect.ValueOf(req))
			}

			results := plan.actionMethod.Func.Call(args)
			if len(results) > 0 {
				return h.toResponse(results[0].Interface())
			}
			return context.NewResponse()
		}

		// 应用控制器级中间件（对应 ThinkPHP 控制器 $middleware 声明）。
		if handlers := h.resolveControllerMiddleware(controllerInstance, parts[1]); len(handlers) > 0 {
			controllerPipeline := middleware.NewPipeline()
			for _, handler := range handlers {
				controllerPipeline.Pipe(handler)
			}
			return controllerPipeline.Then(req, invokeAction)
		}

		return invokeAction(req)
	}

	return h.dispatchInternalError("", fmt.Errorf("invalid route handler"))
}

// controllerMiddlewareProvider 抽象“能声明控制器级中间件”的控制器，
// 通常由嵌入 framework.Controller 自动满足。
type controllerMiddlewareProvider interface {
	GetMiddleware() []framework.ControllerMiddleware
}

// resolveControllerMiddleware 解析控制器声明的中间件，按别名查找处理器，
// 并依据 Only/Except 过滤出对当前动作生效的中间件列表。
func (h *Http) resolveControllerMiddleware(controllerInstance interface{}, action string) []middleware.Handler {
	provider, ok := controllerInstance.(controllerMiddlewareProvider)
	if !ok {
		return nil
	}

	declarations := provider.GetMiddleware()
	if len(declarations) == 0 {
		return nil
	}

	handlers := make([]middleware.Handler, 0, len(declarations))
	for _, declaration := range declarations {
		if !controllerMiddlewareApplies(declaration, action) {
			continue
		}
		handler := h.app.Middleware.ResolveAlias(declaration.Name)
		if handler == nil {
			if h.app.Log != nil {
				h.app.Log.WarningCtx("controller middleware alias not found", map[string]interface{}{
					"alias":  declaration.Name,
					"action": action,
				})
			}
			continue
		}
		handlers = append(handlers, handler)
	}
	return handlers
}

// controllerMiddlewareApplies 判断某条控制器中间件声明是否对指定动作生效。
// Only 非空时仅命中列表内动作；Except 非空时排除列表内动作；动作名大小写不敏感。
func controllerMiddlewareApplies(declaration framework.ControllerMiddleware, action string) bool {
	if len(declaration.Only) > 0 {
		return containsFold(declaration.Only, action)
	}
	if len(declaration.Except) > 0 {
		return !containsFold(declaration.Except, action)
	}
	return true
}

// containsFold 大小写不敏感地判断切片是否包含目标字符串。
func containsFold(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

// reservedControllerMethods 收集嵌入式基类 framework.Controller 暴露的方法名集合。
// 这些方法是框架内置能力（视图渲染、响应辅助、中间件声明等），不应通过自动路由
// 被外部 URL 直接触发。在包初始化时通过反射一次性构建。
var reservedControllerMethods = buildReservedControllerMethods()

func buildReservedControllerMethods() map[string]bool {
	set := make(map[string]bool)
	for _, typ := range []reflect.Type{
		reflect.TypeOf(framework.Controller{}),
		reflect.TypeOf(&framework.Controller{}),
	} {
		for i := 0; i < typ.NumMethod(); i++ {
			set[typ.Method(i).Name] = true
		}
	}
	return set
}

// isReservedControllerMethod 判断方法名是否属于基类内置方法。
func isReservedControllerMethod(name string) bool {
	return reservedControllerMethods[name]
}

// dispatchInternalError 统一记录控制器分发错误，并在生产环境屏蔽内部细节。
func (h *Http) dispatchInternalError(handler string, err error) *context.Response {
	if h.app != nil && h.app.Log != nil && err != nil {
		h.app.Log.ErrorCtx("http dispatch failed", map[string]interface{}{
			"handler": handler,
			"error":   err.Error(),
		})
	}

	message := http.StatusText(http.StatusInternalServerError)
	if h.app != nil && h.app.IsDebug() && err != nil {
		message = err.Error()
	}

	return context.NewResponse().
		Code(http.StatusInternalServerError).
		Content(message)
}

// runTerminators 在响应发出后执行 terminate 回调，回调异常只记录日志，避免破坏已经完成的响应。
func (h *Http) runTerminators(req *context.Request, resp *context.Response, terminators []middleware.Terminator) {
	for _, terminator := range terminators {
		if terminator == nil {
			continue
		}

		func(current middleware.Terminator) {
			defer func() {
				if recovered := recover(); recovered != nil && h.app != nil && h.app.Log != nil {
					h.app.Log.ErrorCtx("http terminator panic", map[string]interface{}{
						"error": fmt.Sprint(recovered),
					})
				}
			}()
			current(req, resp)
		}(terminator)
	}
}

// resolveDispatchPlan 解析或复用控制器分发计划。
func (h *Http) resolveDispatchPlan(handlerKey string, controllerType reflect.Type) (*controllerDispatchPlan, error) {
	h.dispatchPlanMu.RLock()
	if plan, ok := h.dispatchPlans[handlerKey]; ok && plan != nil && plan.controllerType == controllerType {
		h.dispatchPlanMu.RUnlock()
		return plan, nil
	}
	h.dispatchPlanMu.RUnlock()

	parts := strings.Split(handlerKey, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("Invalid route handler format")
	}

	methodName := parts[1]
	actionMethod, ok := controllerType.MethodByName(methodName)
	if !ok {
		return nil, fmt.Errorf("Method not found: %s", methodName)
	}

	var initMethod *reflect.Method
	if method, ok := controllerType.MethodByName("Init"); ok {
		copiedMethod := method
		initMethod = &copiedMethod
	}

	plan := &controllerDispatchPlan{
		controllerType: controllerType,
		actionMethod:   actionMethod,
		initMethod:     initMethod,
	}

	h.dispatchPlanMu.Lock()
	h.dispatchPlans[handlerKey] = plan
	h.dispatchPlanMu.Unlock()
	return plan, nil
}

// toResponse 将控制器返回值转换为框架 Response。
func (h *Http) toResponse(result interface{}) *context.Response {
	if resp, ok := result.(*context.Response); ok {
		return resp
	}
	if str, ok := result.(string); ok {
		return context.NewResponse().Content(str)
	}
	return context.NewResponse().Json(result)
}

// parseMilliseconds 把配置项解析为毫秒级 Duration。
func parseMilliseconds(value interface{}, defaultValue time.Duration) time.Duration {
	parsed := parseInt64(value, int64(defaultValue/time.Millisecond))
	if parsed <= 0 {
		return defaultValue
	}
	return time.Duration(parsed) * time.Millisecond
}

// parseInt 解析配置整数，兼容 int、float64 和字符串。
func parseInt(value interface{}, defaultValue int) int {
	return int(parseInt64(value, int64(defaultValue)))
}

// parseInt64 解析配置整数，兼容常见 JSON 反序列化类型。
func parseInt64(value interface{}, defaultValue int64) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		var parsed int64
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return defaultValue
}
