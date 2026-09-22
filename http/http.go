package http

import (
	stdcontext "context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
)

// Http 是框架 HTTP 内核，启动后配置和路由均保持只读。
type Http struct {
	app           *framework.App
	metadataMu    sync.RWMutex
	name          string
	path          string
	routePath     string
	bind          bool
	route         *route.Router
	middleware    *middleware.Pipeline
	appMiddleware *middleware.Pipeline
	log           *frameworkLog.Log
	metrics       *metrics.Registry
	event         *event.Dispatcher
	telemetry     *telemetry.Tracing
	exception     exception.Handler
	startupOutput io.Writer
	srvConf       serverConf
	compressConf  compressionConf

	dispatchPlanMu      sync.RWMutex
	dispatchPlans       map[string]*controllerDispatchPlan
	routeCallbackPlanMu sync.RWMutex
	routeCallbackPlans  map[reflect.Type]*routeCallbackPlan
	routeFreezeMu       sync.Mutex
	routeFreezeState    atomic.Uint32
	routeFreezeErr      error
	spaIndexMu          sync.RWMutex
	spaIndexCache       []byte
	spaIndexCached      bool
	spaIndexAt          time.Time
	staticMissMu        sync.RWMutex
	staticMisses        map[string]time.Time
	initializeOnce      sync.Once
	initializeErr       error
	listenMu            sync.Mutex
	listening           bool
	allowApplications   bool
	applicationHost     *nativeApplicationHost
	endStateMu          sync.Mutex
	endStates           map[*context.ResponseIdentity]*requestEndState
	requestTasks        map[*context.Request]requestTaskEntry
}

const (
	routeFreezePending uint32 = iota
	routeFreezeReady
	routeFreezeFailed
)

type requestServeState struct {
	raw               *http.Request
	req               *context.Request
	response          *context.Response
	statusWriter      *statusTrackingResponseWriter
	writer            http.ResponseWriter
	compressionWriter *CompressionResponseWriter
	startedAt         time.Time
	metricsRegistry   *metrics.Registry
	metricsActive     bool
	transmissionErr   error
	abortResponse     bool
	task              *framework.RequestTask
	statusFacade      adaptiveWriterStorage
	compressionFacade adaptiveWriterStorage
	headFacade        adaptiveWriterStorage
}

// httpServiceSnapshot 是已通过类型校验的 HTTP 运行期依赖集合。
// 监听租约获取后只重新发布该快照，不回写已准备的服务器配置。
type httpServiceSnapshot struct {
	route         *route.Router
	middleware    *middleware.Pipeline
	appMiddleware *middleware.Pipeline
	log           *frameworkLog.Log
	metrics       *metrics.Registry
	event         *event.Dispatcher
	telemetry     *telemetry.Tracing
	exception     exception.Handler
}

// NewHttp 创建 HTTP 内核。
// 未初始化的 App 保持延迟初始化，第一次请求或服务器运行时才加载配置并启动服务。
func NewHttp(app *framework.App) (*Http, error) {
	return newHttp(app, true)
}

// newHttp 创建 HTTP 内核；应用子处理器关闭再次构建应用宿主，避免递归装配。
func newHttp(app *framework.App, allowApplications bool) (*Http, error) {
	if app == nil {
		return nil, framework.ErrNilApplication
	}
	handler := &Http{
		app:                app,
		routePath:          app.GetRoutePath(),
		startupOutput:      os.Stdout,
		dispatchPlans:      make(map[string]*controllerDispatchPlan),
		routeCallbackPlans: make(map[reflect.Type]*routeCallbackPlan),
		staticMisses:       make(map[string]time.Time),
		endStates:          make(map[*context.ResponseIdentity]*requestEndState),
		requestTasks:       make(map[*context.Request]requestTaskEntry),
		allowApplications:  allowApplications,
	}
	// 已显式初始化的 App 继续在构造期报告配置错误，兼容嵌入式宿主；
	// NewApp 创建的默认应用则完全保持延迟初始化。
	state := app.State()
	if app.Initialized() && (state == framework.ApplicationStateInitialized || state == framework.ApplicationStateRunning) {
		if err := handler.loadServices(); err != nil {
			return nil, err
		}
	}
	return handler, nil
}

// ensureInitialized 按 ThinkPHP Http::initialize 语义一次性初始化 App 并启动服务。
func (h *Http) ensureInitialized() error {
	if h == nil || h.app == nil {
		return errors.New("HTTP 内核未初始化")
	}
	h.initializeOnce.Do(func() {
		if h.allowApplications && len(h.app.ApplicationNames()) > 0 && h.GetPath() != "" {
			if h.GetName() == "" {
				h.initializeErr = fmt.Errorf("Http.Path 必须与 Http.Name 一起使用")
				return
			}
			if err := h.app.ConfigureApplicationPath(h.GetName(), h.GetPath()); err != nil {
				h.initializeErr = fmt.Errorf("绑定应用路径失败: %w", err)
				return
			}
		}
		if err := h.app.EnsureReady(); err != nil {
			h.initializeErr = fmt.Errorf("准备 HTTP 应用失败: %w", err)
			return
		}
		// Provider Boot 允许替换服务；构造期校验的快照不能直接进入运行期。
		h.initializeErr = h.loadServices()
		if h.initializeErr == nil && h.allowApplications && len(h.app.ApplicationNames()) > 0 {
			h.applicationHost, h.initializeErr = newNativeApplicationHost(h)
		}
	})
	return h.initializeErr
}

// loadServices 从完成初始化的 App 取得 HTTP 所需服务和严格服务器配置。
func (h *Http) loadServices() error {
	parseConfiguration := parseHTTPConfig
	if h.allowApplications && len(h.app.ApplicationNames()) > 0 {
		parseConfiguration = parseProjectHTTPConfig
	}
	serverConfig, compressionConfig, err := parseConfiguration(h.app)
	if err != nil {
		return err
	}
	snapshot, err := resolveHTTPServiceSnapshot(h.app)
	if err != nil {
		return err
	}
	h.applyHTTPServiceSnapshot(snapshot)
	h.srvConf = serverConfig
	h.compressConf = compressionConfig
	return nil
}

// reloadRuntimeServices 在应用运行租约冻结变更后重发布最终服务快照。
func (h *Http) reloadRuntimeServices() error {
	if h == nil || h.app == nil {
		return errors.New("HTTP 内核未初始化")
	}
	snapshot, err := resolveHTTPServiceSnapshot(h.app)
	if err != nil {
		return err
	}
	h.applyHTTPServiceSnapshot(snapshot)
	return nil
}

func resolveHTTPServiceSnapshot(app *framework.App) (httpServiceSnapshot, error) {
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		return httpServiceSnapshot{}, fmt.Errorf("解析 HTTP 路由服务失败: %w", err)
	}
	pipeline, err := framework.ResolveServiceAs[*middleware.Pipeline](app, framework.ServiceMiddleware)
	if err != nil {
		return httpServiceSnapshot{}, fmt.Errorf("解析 HTTP 中间件服务失败: %w", err)
	}
	logger, err := framework.ResolveServiceAs[*frameworkLog.Log](app, framework.ServiceLog)
	if err != nil {
		return httpServiceSnapshot{}, fmt.Errorf("解析 HTTP 日志服务失败: %w", err)
	}
	metricsRegistry, err := framework.ResolveServiceAs[*metrics.Registry](app, framework.ServiceMetrics)
	if err != nil {
		return httpServiceSnapshot{}, fmt.Errorf("解析 HTTP 指标服务失败: %w", err)
	}
	dispatcher, err := framework.ResolveServiceAs[*event.Dispatcher](app, framework.ServiceEvent)
	if err != nil {
		return httpServiceSnapshot{}, fmt.Errorf("解析 HTTP 事件服务失败: %w", err)
	}
	tracing, err := framework.ResolveServiceAs[*telemetry.Tracing](app, framework.ServiceTelemetry)
	if err != nil {
		return httpServiceSnapshot{}, fmt.Errorf("解析 HTTP 遥测服务失败: %w", err)
	}
	exceptionHandler, err := framework.ResolveServiceAs[exception.Handler](app, framework.ServiceExceptionHandle)
	if err != nil {
		return httpServiceSnapshot{}, fmt.Errorf("解析 HTTP 异常处理器失败: %w", err)
	}
	return httpServiceSnapshot{
		route:         router,
		middleware:    pipeline,
		appMiddleware: app.ApplicationMiddleware(),
		log:           logger,
		metrics:       metricsRegistry,
		event:         dispatcher,
		telemetry:     tracing,
		exception:     exceptionHandler,
	}, nil
}

func (h *Http) applyHTTPServiceSnapshot(snapshot httpServiceSnapshot) {
	h.route = snapshot.route
	h.middleware = snapshot.middleware
	h.appMiddleware = snapshot.appMiddleware
	h.log = snapshot.log
	h.metrics = snapshot.metrics
	h.event = snapshot.event
	h.telemetry = snapshot.telemetry
	h.exception = snapshot.exception
}

// ServeHTTP 依次执行入口校验、结构化输入解析、静态资源、路由与中间件流水线。
func (h *Http) ServeHTTP(originalWriter http.ResponseWriter, raw *http.Request) {
	h.serveHTTP(originalWriter, raw)
}

func (h *Http) serveHTTP(originalWriter http.ResponseWriter, raw *http.Request) {
	if isNilHTTPResponseWriter(originalWriter) {
		return
	}
	if h != nil && h.app != nil && h.allowApplications && len(h.app.ApplicationNames()) > 0 {
		if err := h.ensureInitialized(); err != nil {
			h.logHTTPError("初始化多应用 HTTP 宿主失败", err)
			http.Error(originalWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if h.applicationHost != nil {
			h.applicationHost.serveHTTP(originalWriter, raw)
			return
		}
	}
	statusWriter := newStatusTrackingResponseWriter(originalWriter)
	if raw != nil && raw.Method == http.MethodHead {
		statusWriter.SuppressBody(true)
	}
	state := &requestServeState{
		raw:          raw,
		statusWriter: statusWriter,
		startedAt:    time.Now(),
	}
	state.writer = state.statusFacade.adapt(statusWriter, originalWriter)
	defer func() {
		recovered := recover()
		if recovered == http.ErrAbortHandler {
			state.abortResponse = true
		}
		h.finishRequest(state, recovered)
		if state.abortResponse {
			// 清理完成后交由 net/http 或 HTTP/3 宿主中止传输，禁止输出正常结束标记。
			panic(http.ErrAbortHandler)
		}
	}()

	if h == nil || h.app == nil {
		http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if err := h.ensureInitialized(); err != nil {
		h.logHTTPError("初始化 HTTP 应用失败", err)
		http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if h.metrics != nil {
		state.metricsRegistry = h.metrics
		state.metricsActive = state.metricsRegistry.Begin()
	}
	if raw == nil || raw.URL == nil {
		http.Error(state.writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if h.telemetry != nil && h.telemetry.Enabled() {
		traceContext, _ := h.telemetry.StartServer(raw.Context(), raw.Header, raw.Method, raw.URL.Path, raw.Host)
		raw = raw.WithContext(traceContext)
		state.raw = raw
	}
	if !h.isAllowedHost(raw.Host) {
		http.Error(state.writer, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
		return
	}
	task, err := h.acquireRequestTask(raw)
	if err != nil {
		state.writer.Header().Set("Retry-After", "1")
		http.Error(state.writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	state.task = task
	requestScope, err := h.app.NewScope()
	if err != nil {
		h.logHTTPError("创建请求服务作用域失败", err)
		http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if h.srvConf.EnableTLS && h.srvConf.EnableHTTP3 {
		state.writer.Header().Set("Alt-Svc", fmt.Sprintf(`h3=":%d"; ma=2592000`, h.srvConf.Port))
	}
	if h.compressConf.Enable && raw.Method != http.MethodHead {
		state.compressionWriter = newConfiguredCompressionResponseWriter(
			state.writer,
			raw,
			h.compressConf.MinSize,
			h.compressConf.Levels,
		)
		state.writer = state.compressionFacade.adapt(state.compressionWriter, originalWriter)
	}
	if raw.Method == http.MethodHead {
		headWriter := &headResponseWriter{ResponseWriter: state.writer}
		state.writer = state.headFacade.adapt(headWriter, originalWriter)
	}
	req, err := h.newRequest(
		requestScope,
		raw,
		context.WithTrustedProxySet(h.srvConf.TrustedProxySet),
		context.WithMultipartMemoryLimit(h.srvConf.MultipartMemory),
		context.WithMaxBodyBytes(h.srvConf.MaxBodyBytes),
		context.WithResponseWriter(state.writer),
		context.WithEnvService(h.app.Env()),
		context.WithTrustedIdleServiceScope(requestScope, requestScope),
	)
	if err != nil {
		if closeErr := requestScope.Close(); closeErr != nil {
			h.logHTTPError("关闭请求服务作用域失败", closeErr)
		}
		h.logHTTPError("创建请求上下文失败", err)
		http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	state.req = req
	h.rememberRequestTask(req, task, true)
	if application, exists := resolvedApplication(raw); exists {
		req.SetApplicationContext(application)
	}
	state.response = h.Run(req)
	// ServeContent 可能依据条件请求返回 206/304；把真实状态回写到同一
	// Response，确保随后 Http.End 收到的状态与客户端一致。
	if state.response != nil && state.response.Committed() && state.statusWriter.Written() {
		state.response.Code(state.statusWriter.Status())
	}
	if state.response.Committed() || state.statusWriter.Written() {
		return
	}
	if err = state.response.Send(state.writer); err != nil {
		state.transmissionErr = err
		h.logHTTPError("发送 HTTP 响应失败", err)
		if context.IsResponseTransmissionError(err) {
			if state.statusWriter.Written() {
				state.abortResponse = true
			} else {
				h.replaceUncommittedSendFailure(state)
			}
		}
	}
}

func (h *Http) replaceUncommittedSendFailure(state *requestServeState) {
	if state == nil || state.statusWriter == nil || state.statusWriter.Written() {
		return
	}
	if state.compressionWriter != nil && !state.compressionWriter.ResetUncommitted() {
		return
	}
	previous := state.response
	fallback := context.NewResponse().
		Header("Content-Type", "text/plain; charset=utf-8").
		Header("X-Content-Type-Options", "nosniff").
		Header("Cache-Control", "no-store").
		Code(http.StatusInternalServerError).
		Content(http.StatusText(http.StatusInternalServerError))
	state.response = fallback
	h.transferRequestEndState(previous, fallback)
	if err := fallback.Send(state.writer); err != nil {
		h.logHTTPError("发送 HTTP 降级响应失败", err)
	}
}

func (h *Http) routeRequest(req *context.Request) *context.Response {
	if err := h.ensureRouteFrozen(); err != nil {
		h.logHTTPError("加载并冻结路由失败", err)
		return context.NewResponse().
			Code(http.StatusInternalServerError).
			Content(http.StatusText(http.StatusInternalServerError))
	}
	matched, params, err := h.route.Match(req)
	if err != nil {
		return h.responseForRouteError(err)
	}
	for key, value := range params {
		req.SetRoute(key, value)
	}
	if matched == nil {
		if (req.Method() == http.MethodGet || req.Method() == http.MethodHead) && !isAPIPath(req.Path()) {
			if content, ok := h.spaIndexContent(); ok {
				return context.NewResponse().
					Header("Content-Type", "text/html; charset=utf-8").
					Content(string(content))
			}
		}
		return context.NewResponse().Code(http.StatusNotFound).Content("404 Not Found")
	}
	if h.telemetry != nil && h.telemetry.Enabled() {
		spanRoute := matched.Path()
		telemetry.SetHTTPRoute(req.Raw().Context(), spanRoute)
	}

	if !matched.HasMiddleware() {
		return h.dispatch(matched, req)
	}
	return matched.ExecuteMiddleware(req, func(current *context.Request) *context.Response {
		return h.dispatch(matched, current)
	})
}

func (h *Http) responseForRouteError(err error) *context.Response {
	if errors.Is(err, route.ErrInvalidRequestPath) || errors.Is(err, route.ErrNilRequest) {
		return context.NewResponse().Code(http.StatusBadRequest).Content(http.StatusText(http.StatusBadRequest))
	}
	h.logHTTPError("路由匹配失败", err)
	return context.NewResponse().
		Code(http.StatusInternalServerError).
		Content(http.StatusText(http.StatusInternalServerError))
}

func (h *Http) writeRequestParseError(writer http.ResponseWriter, err error) {
	response := responseForRequestParseError(err)
	if sendErr := response.Send(writer); sendErr != nil {
		h.logHTTPError("发送请求解析错误响应失败", sendErr)
	}
}

func responseForRequestParseError(err error) *context.Response {
	if errors.Is(err, context.ErrRequestBodyTooLarge) {
		return context.NewResponse().
			Code(http.StatusRequestEntityTooLarge).
			Content(http.StatusText(http.StatusRequestEntityTooLarge))
	}
	return context.NewResponse().Code(http.StatusBadRequest).Content(http.StatusText(http.StatusBadRequest))
}

func (h *Http) finishRequest(state *requestServeState, recovered interface{}) {
	if state == nil {
		return
	}
	if state.req == nil {
		defer state.task.Release()
	}
	boundedEnd := h.requestEndRequiresSupervisor(state)
	endContext := stdcontext.Background()
	cancelEnd := func() {}
	if boundedEnd {
		requestEndTimeout := defaultRequestEndTimeout
		if h != nil {
			requestEndTimeout = h.srvConf.RequestEndTimeout
		}
		endContext, cancelEnd = requestEndContext(state.req, requestEndTimeout)
	}
	defer cancelEnd()
	requestErr := state.transmissionErr
	if recovered != nil && recovered != http.ErrAbortHandler {
		requestErr = fmt.Errorf("HTTP 请求 panic: %v", recovered)
	}
	// 指标和服务端 Span 必须覆盖完整请求生命周期，但不能被扩展回调的
	// panic 绕过；请求收尾自身有独立截止时间，因此该 defer 最终必定执行。
	defer func() {
		if state.metricsRegistry != nil {
			state.metricsRegistry.End(state.metricsActive, state.statusWriter.Status(), time.Since(state.startedAt))
		}
		if h != nil && h.telemetry != nil && h.telemetry.Enabled() && state.raw != nil {
			telemetry.EndHTTPServer(state.raw.Context(), state.statusWriter.Status(), requestErr)
		}
	}()
	if recovered != nil && recovered != http.ErrAbortHandler {
		if !state.statusWriter.Written() {
			state.statusWriter.ResetUncommitted()
			if state.compressionWriter != nil {
				state.compressionWriter.ResetUncommitted()
			}
			if err := h.renderRecoveredException(state.writer, state.raw, recovered); err != nil {
				requestErr = errors.Join(requestErr, err)
				if boundedEnd {
					requestErr = errors.Join(requestErr, h.logHTTPErrorContext(endContext, "异常渲染失败", err))
				} else {
					requestErr = errors.Join(requestErr, h.logHTTPErrorNonBlocking("异常渲染失败", err))
				}
				state.statusWriter.ResetUncommitted()
				if state.compressionWriter != nil {
					state.compressionWriter.ResetUncommitted()
				}
				if !state.statusWriter.Written() {
					http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}
		} else {
			state.abortResponse = true
			panicErr := fmt.Errorf("%v", recovered)
			if boundedEnd {
				requestErr = errors.Join(requestErr, h.logHTTPErrorContext(endContext, "响应写出后发生异常", panicErr))
			} else {
				requestErr = errors.Join(requestErr, h.logHTTPErrorNonBlocking("响应写出后发生异常", panicErr))
			}
		}
	}
	if state.compressionWriter != nil && state.abortResponse {
		state.compressionWriter.Abort()
	}
	if state.compressionWriter != nil && !state.statusWriter.Hijacked() && !state.abortResponse {
		if err := safeCloseCompressionWriter(state.compressionWriter); err != nil {
			state.abortResponse = state.statusWriter.Written()
			requestErr = errors.Join(requestErr, err)
			if boundedEnd {
				requestErr = errors.Join(requestErr, h.logHTTPErrorContext(endContext, "关闭响应压缩器失败", err))
			} else {
				requestErr = errors.Join(requestErr, h.logHTTPErrorNonBlocking("关闭响应压缩器失败", err))
			}
		}
	}
	if !state.statusWriter.Hijacked() && !state.abortResponse {
		state.statusWriter.CommitEmpty()
	}

	var accessLogErr error
	if boundedEnd {
		accessLogErr = h.writeAccessLogContext(endContext, state)
	} else {
		accessLogErr = h.writeAccessLogNonBlocking(state)
	}
	if accessLogErr != nil {
		requestErr = errors.Join(requestErr, wrapRequestEndError("访问日志入队", accessLogErr))
	}
	terminatorResponse := state.response
	// 文件、流和提交钩子都可能在 Send 内改变最终状态，结束阶段统一使用真实传输结果。
	if terminatorResponse != nil && state.statusWriter.Written() {
		terminatorResponse.Code(state.statusWriter.Status())
	}
	if terminatorResponse == nil && state.req != nil {
		terminatorResponse = context.NewCommittedResponse(state.statusWriter.Status())
		h.rememberRequestEndState(terminatorResponse, state.req)
	}
	if terminatorResponse != nil {
		var endErr error
		if boundedEnd {
			endErr = h.endWithContext(endContext, terminatorResponse)
		} else {
			endErr = h.endRequestInline(terminatorResponse)
		}
		requestErr = errors.Join(requestErr, endErr)
		if endErr != nil {
			if boundedEnd {
				requestErr = errors.Join(requestErr, h.logHTTPErrorContext(endContext, "HTTP 请求收尾失败", endErr))
			} else {
				requestErr = errors.Join(requestErr, h.logHTTPErrorNonBlocking("HTTP 请求收尾失败", endErr))
			}
		}
	}
}

// ensureRouteFrozen 在启动阶段或直接 ServeHTTP 的首个请求中冻结路由，成功后仅执行原子读。
// requestEventContext 为 HTTP 生命周期事件提取请求上下文；结束事件忽略取消以确保清理监听器仍会执行。
func requestEventContext(raw *http.Request, terminal bool) stdcontext.Context {
	requestContext := stdcontext.Background()
	if raw != nil && raw.Context() != nil {
		requestContext = raw.Context()
	}
	if terminal {
		return stdcontext.WithoutCancel(requestContext)
	}
	return requestContext
}

func (h *Http) ensureRouteFrozen() error {
	if h == nil || h.route == nil {
		return errors.New("HTTP 路由器不能为空")
	}
	if h.routeFreezeState.Load() == routeFreezeReady {
		return nil
	}
	h.routeFreezeMu.Lock()
	defer h.routeFreezeMu.Unlock()
	switch h.routeFreezeState.Load() {
	case routeFreezeReady:
		return nil
	case routeFreezeFailed:
		return h.routeFreezeErr
	}
	if h.app == nil {
		h.routeFreezeErr = errors.New("HTTP 应用不能为空")
		h.routeFreezeState.Store(routeFreezeFailed)
		return h.routeFreezeErr
	}
	if h.app.WithRoute() {
		if err := h.app.LoadRoutes(); err != nil {
			h.routeFreezeErr = err
			h.routeFreezeState.Store(routeFreezeFailed)
			return h.routeFreezeErr
		}
	}
	h.routeFreezeErr = h.route.Freeze()
	if h.routeFreezeErr != nil {
		h.routeFreezeState.Store(routeFreezeFailed)
		return h.routeFreezeErr
	}
	h.routeFreezeState.Store(routeFreezeReady)
	return nil
}

func (h *Http) renderRecoveredException(writer http.ResponseWriter, raw *http.Request, recovered interface{}) (err error) {
	return exception.ReportAndRender(h.recoveredExceptionHandler(), writer, raw, recovered)
}

func (h *Http) recoveredExceptionHandler() exception.Handler {
	handler := h.exception
	if handler == nil {
		templatePath := ""
		if h.app != nil {
			templatePath = h.app.ExceptionTemplatePath()
		}
		handler = &exception.Handle{
			App:    h.app,
			Log:    h.log,
			TplDir: templatePath,
		}
	}
	return handler
}

func safeCloseCompressionWriter(writer *CompressionResponseWriter) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("关闭压缩器 panic: %v", recovered)
		}
	}()
	return writer.Close()
}

func (h *Http) writeAccessLogContext(ctx stdcontext.Context, state *requestServeState) error {
	return h.writeAccessLogRecord(ctx, state, false)
}

func (h *Http) writeAccessLogNonBlocking(state *requestServeState) error {
	return h.writeAccessLogRecord(stdcontext.Background(), state, true)
}

func (h *Http) writeAccessLogRecord(ctx stdcontext.Context, state *requestServeState, nonBlocking bool) error {
	if h == nil || h.log == nil || !h.log.IsLevelEnabled(frameworkLog.LevelInfo) {
		return nil
	}
	if state == nil || state.raw == nil {
		return nil
	}
	path := ""
	path = requestLogPath(state.raw)
	ip := ""
	if state.req != nil {
		ip = state.req.Ip()
	}
	duration := time.Since(state.startedAt)
	message := fmt.Sprintf("%s %s %d %.3fms", state.raw.Method, path, state.statusWriter.Status(), float64(duration.Microseconds())/1000)
	fields := map[string]interface{}{
		"method":      state.raw.Method,
		"path":        path,
		"status":      state.statusWriter.Status(),
		"duration_ms": float64(duration.Microseconds()) / 1000,
		"ip":          ip,
		"user_agent":  state.raw.UserAgent(),
	}
	if nonBlocking {
		return h.log.RecordContextNonBlocking(ctx, message, frameworkLog.LevelInfo, fields)
	}
	return h.log.RecordContext(ctx, message, frameworkLog.LevelInfo, fields)
}

// requestLogPath 保留 URL 转义形式并兜底转义换行，防止访问日志被请求路径伪造分行。
func requestLogPath(raw *http.Request) string {
	if raw == nil || raw.URL == nil {
		return ""
	}
	path := raw.URL.EscapedPath()
	path = strings.ReplaceAll(path, "\r", "%0D")
	return strings.ReplaceAll(path, "\n", "%0A")
}

func (h *Http) logHTTPError(message string, err error) {
	if h != nil && h.log != nil && err != nil {
		h.log.ErrorCtx(message, map[string]interface{}{"error": frameworkLog.SanitizeErrorText(err.Error())})
	}
}

// logHTTPErrorContext 在请求收尾截止边界内记录错误；队列持续饱和时返回
// 可观测拒绝错误，不能为了记录收尾失败再次阻塞请求 goroutine。
func (h *Http) logHTTPErrorContext(ctx stdcontext.Context, message string, err error) error {
	return h.logHTTPErrorRecord(ctx, message, err, false)
}

func (h *Http) logHTTPErrorNonBlocking(message string, err error) error {
	return h.logHTTPErrorRecord(stdcontext.Background(), message, err, true)
}

func (h *Http) logHTTPErrorRecord(ctx stdcontext.Context, message string, err error, nonBlocking bool) error {
	if h == nil || h.log == nil || err == nil {
		return nil
	}
	fields := map[string]interface{}{"error": frameworkLog.SanitizeErrorText(err.Error())}
	if nonBlocking {
		return h.log.RecordContextNonBlocking(ctx, message, frameworkLog.LevelError, fields)
	}
	return h.log.RecordContext(ctx, message, frameworkLog.LevelError, fields)
}

func (h *Http) runTerminators(
	ctx stdcontext.Context,
	req *context.Request,
	resp *context.Response,
	terminators []middleware.TerminationCallback,
) error {
	var terminateErr error
	for index, callback := range terminators {
		err := invokeRequestTerminator(ctx, callback, req, resp)
		if err != nil {
			terminateErr = errors.Join(
				terminateErr,
				fmt.Errorf("第 %d 个 HTTP terminate 回调失败: %w", index+1, err),
			)
		}
	}
	return terminateErr
}

func invokeRequestTerminator(
	ctx stdcontext.Context,
	callback middleware.TerminationCallback,
	req *context.Request,
	resp *context.Response,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("HTTP terminate 回调 panic: %v", recovered)
		}
	}()
	callback.Invoke(ctx, req, resp)
	return nil
}

func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}
