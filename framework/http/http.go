package http

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"thinkgo/framework"
	"thinkgo/framework/context"
	"thinkgo/framework/event"
	"thinkgo/framework/exception"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
)

// Http 是框架 HTTP 内核，启动后配置和路由均保持只读。
type Http struct {
	app          *framework.App
	srvConf      serverConf
	compressConf compressionConf

	dispatchPlanMu sync.RWMutex
	dispatchPlans  map[string]*controllerDispatchPlan
	spaIndexMu     sync.RWMutex
	spaIndexCache  []byte
	spaIndexCached bool
	spaIndexAt     time.Time
	staticMissMu   sync.RWMutex
	staticMisses   map[string]time.Time
}

type requestServeState struct {
	raw               *http.Request
	req               *context.Request
	response          *context.Response
	statusWriter      *statusTrackingResponseWriter
	writer            http.ResponseWriter
	compressionWriter *CompressionResponseWriter
	startedAt         time.Time
}

// NewHttp 严格解析配置并创建 HTTP 内核，任何非法安全边界都会直接返回错误。
func NewHttp(app *framework.App) (*Http, error) {
	serverConfig, compressionConfig, err := parseHTTPConfig(app)
	if err != nil {
		return nil, err
	}
	return &Http{
		app:           app,
		srvConf:       serverConfig,
		compressConf:  compressionConfig,
		dispatchPlans: make(map[string]*controllerDispatchPlan),
		staticMisses:  make(map[string]time.Time),
	}, nil
}

// ServeHTTP 依次执行入口校验、结构化输入解析、静态资源、路由与中间件流水线。
func (h *Http) ServeHTTP(originalWriter http.ResponseWriter, raw *http.Request) {
	statusWriter := newStatusTrackingResponseWriter(originalWriter)
	state := &requestServeState{
		raw:          raw,
		statusWriter: statusWriter,
		writer:       statusWriter,
		startedAt:    time.Now(),
	}
	defer func() {
		h.finishRequest(state, recover())
	}()

	if h == nil || h.app == nil {
		http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if raw == nil || raw.URL == nil {
		http.Error(state.writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !h.isAllowedHost(raw.Host) {
		http.Error(state.writer, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
		return
	}
	if err := h.app.Route.Freeze(); err != nil {
		h.logHTTPError("冻结路由失败", err)
		http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	req, err := context.NewRequest(
		raw,
		context.WithTrustedProxies(h.srvConf.TrustedProxies),
		context.WithMultipartMemoryLimit(h.srvConf.MultipartMemory),
		context.WithMaxBodyBytes(h.srvConf.MaxBodyBytes),
	)
	if err != nil {
		h.logHTTPError("创建请求上下文失败", err)
		http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	state.req = req
	if err = req.Parse(); err != nil {
		h.writeRequestParseError(state.writer, err)
		return
	}

	if h.app.Event != nil {
		if err = h.app.Event.Dispatch(event.NewHttpRunEvent()); err != nil {
			panic(err)
		}
	}
	if h.srvConf.EnableTLS && h.srvConf.EnableHTTP3 {
		state.writer.Header().Set("Alt-Svc", fmt.Sprintf(`h3=":%d"; ma=2592000`, h.srvConf.Port))
	}
	if h.compressConf.Enable && raw.Method != http.MethodHead {
		state.compressionWriter = NewCompressionResponseWriter(
			state.writer,
			raw,
			h.compressConf.MinSize,
			h.compressConf.Levels,
		)
		state.writer = state.compressionWriter
	}

	if raw.Method == http.MethodGet || raw.Method == http.MethodHead {
		staticPath := raw.URL.Path
		if staticPath == "/" {
			staticPath = "/index.html"
		}
		if h.servePublicFile(state.writer, raw, staticPath) {
			return
		}
	}

	response := h.app.Middleware.Then(req, func(current *context.Request) *context.Response {
		return h.routeRequest(current)
	})
	if response == nil {
		response = context.NewResponse().
			Code(http.StatusInternalServerError).
			Content(http.StatusText(http.StatusInternalServerError))
	}
	state.response = response

	if parseErr := req.ParseError(); parseErr != nil {
		state.response = responseForRequestParseError(parseErr)
	}
	if raw.Method == http.MethodHead {
		state.writer = &headResponseWriter{ResponseWriter: state.writer}
	}
	if err = state.response.Send(state.writer); err != nil {
		h.logHTTPError("发送 HTTP 响应失败", err)
		if context.IsResponseTransmissionError(err) {
			h.replaceUncommittedSendFailure(state)
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
	fallback := context.NewResponse().
		Header("Content-Type", "text/plain; charset=utf-8").
		Header("X-Content-Type-Options", "nosniff").
		Header("Cache-Control", "no-store").
		Code(http.StatusInternalServerError).
		Content(http.StatusText(http.StatusInternalServerError))
	state.response = fallback
	if err := fallback.Send(state.writer); err != nil {
		h.logHTTPError("发送 HTTP 降级响应失败", err)
	}
}

func (h *Http) routeRequest(req *context.Request) *context.Response {
	matched, params, err := h.app.Route.Match(req)
	if err != nil {
		return h.responseForRouteError(err)
	}
	for key, value := range params {
		req.Set(key, value)
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

	handlers := matched.Middlewares()
	if len(handlers) == 0 {
		return h.dispatch(matched, req)
	}
	routePipeline := middleware.NewPipeline()
	for _, handler := range handlers {
		routePipeline.Pipe(handler)
	}
	return routePipeline.Then(req, func(current *context.Request) *context.Response {
		return h.dispatch(matched, current)
	})
}

func (h *Http) responseForRouteError(err error) *context.Response {
	var methodErr *route.MethodNotAllowedError
	if errors.As(err, &methodErr) {
		return context.NewResponse().
			Header("Allow", strings.Join(methodErr.Allowed, ", ")).
			Code(http.StatusMethodNotAllowed).
			Content(http.StatusText(http.StatusMethodNotAllowed))
	}
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
	if recovered != nil {
		if !state.statusWriter.Written() {
			if state.compressionWriter != nil {
				state.compressionWriter.ResetUncommitted()
			}
			if err := h.renderRecoveredException(state.writer, state.raw, recovered); err != nil {
				h.logHTTPError("异常渲染失败", err)
				if state.compressionWriter != nil {
					state.compressionWriter.ResetUncommitted()
				}
				if !state.statusWriter.Written() {
					http.Error(state.writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}
		} else {
			h.logHTTPError("响应写出后发生异常", fmt.Errorf("%v", recovered))
		}
	}
	if state.compressionWriter != nil {
		if err := safeCloseCompressionWriter(state.compressionWriter); err != nil {
			h.logHTTPError("关闭响应压缩器失败", err)
		}
	}

	if state.req != nil {
		terminatorResponse := state.response
		if terminatorResponse == nil {
			terminatorResponse = context.NewResponse().Code(state.statusWriter.Status())
		}
		h.runTerminators(state.req, terminatorResponse, middleware.RequestTerminators(state.req))
		if err := state.req.Cleanup(); err != nil {
			h.logHTTPError("清理请求临时资源失败", err)
		}
	}
	h.writeAccessLog(state)
	if h != nil && h.app != nil && h.app.Event != nil {
		if err := h.app.Event.Dispatch(event.NewHttpEndEvent(state.statusWriter.Status())); err != nil {
			h.logHTTPError("HTTP 结束事件分发失败", err)
		}
	}
}

func (h *Http) renderRecoveredException(writer http.ResponseWriter, raw *http.Request, recovered interface{}) (err error) {
	defer func() {
		if nested := recover(); nested != nil {
			err = fmt.Errorf("异常渲染 panic: %v", nested)
		}
	}()
	handler := &exception.Handle{
		App:    h.app,
		Log:    h.app.Log,
		TplDir: filepath.Join(h.app.BasePath, "framework", "exception", "tpl"),
	}
	return handler.Render(writer, raw, recovered)
}

func safeCloseCompressionWriter(writer *CompressionResponseWriter) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("关闭压缩器 panic: %v", recovered)
		}
	}()
	return writer.Close()
}

func (h *Http) writeAccessLog(state *requestServeState) {
	if h == nil || h.app == nil || h.app.Log == nil || state.raw == nil {
		return
	}
	path := ""
	path = requestLogPath(state.raw)
	ip := ""
	if state.req != nil {
		ip = state.req.Ip()
	}
	duration := time.Since(state.startedAt)
	h.app.Log.InfoCtx(
		fmt.Sprintf("%s %s %d %.3fms", state.raw.Method, path, state.statusWriter.Status(), float64(duration.Microseconds())/1000),
		map[string]interface{}{
			"method":      state.raw.Method,
			"path":        path,
			"status":      state.statusWriter.Status(),
			"duration_ms": float64(duration.Microseconds()) / 1000,
			"ip":          ip,
			"user_agent":  state.raw.UserAgent(),
		},
	)
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
	if h != nil && h.app != nil && h.app.Log != nil && err != nil {
		h.app.Log.ErrorCtx(message, map[string]interface{}{"error": err.Error()})
	}
}

func (h *Http) runTerminators(req *context.Request, resp *context.Response, terminators []middleware.Terminator) {
	for _, terminator := range terminators {
		if terminator == nil {
			continue
		}
		func(current middleware.Terminator) {
			defer func() {
				if recovered := recover(); recovered != nil {
					h.logHTTPError("HTTP terminate 回调异常", fmt.Errorf("%v", recovered))
				}
			}()
			current(req, resp)
		}(terminator)
	}
}

func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}
