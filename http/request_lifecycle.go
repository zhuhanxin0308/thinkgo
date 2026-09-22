package http

import (
	stdcontext "context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

type requestEndState struct {
	request     *fwcontext.Request
	terminators []middleware.TerminationCallback
	task        *framework.RequestTask
}

// Run 执行一次应用请求并返回 Response，对应 ThinkPHP Http::run。
// 省略请求或显式传 nil 时创建默认请求；响应发送由宿主负责，发送后必须调用 End。
func (h *Http) Run(requests ...*fwcontext.Request) (response *fwcontext.Response) {
	if len(requests) > 1 {
		panic(fmt.Errorf("Http.Run 最多只能接收一个 Request"))
	}
	var request *fwcontext.Request
	if len(requests) == 1 {
		request = requests[0]
	}
	delegated := false

	defer func() {
		if delegated {
			return
		}
		recovered := recover()
		if recovered == http.ErrAbortHandler {
			response = fwcontext.NewCommittedResponse(http.StatusOK)
		} else if recovered != nil {
			response = h.responseForRecoveredRequest(request, recovered)
		}
		if response == nil {
			response = internalServerErrorResponse()
		}
		if recovered == http.ErrAbortHandler {
			// 直接 Run 没有可交给调用方 End 的返回值，必须在透传中断信号前收尾。
			if !h.requestTaskHostManaged(request) {
				h.rememberRequestEndState(response, request)
				h.End(response)
			}
			panic(http.ErrAbortHandler)
		}
		h.rememberRequestEndState(response, request)
	}()

	if h == nil || h.app == nil {
		return internalServerErrorResponse()
	}
	if err := h.ensureInitialized(); err != nil {
		h.logHTTPError("初始化 HTTP 应用失败", err)
		return internalServerErrorResponse()
	}
	if h.applicationHost != nil {
		response, delegated = h.applicationHost.run(request)
		return response
	}
	if request == nil {
		var err error
		request, err = h.newDefaultRequest()
		if err != nil {
			h.logHTTPError("创建默认请求失败", err)
			return internalServerErrorResponse()
		}
	}

	raw := request.Raw()
	if raw == nil || raw.URL == nil {
		return fwcontext.NewResponse().Code(http.StatusBadRequest).Content(http.StatusText(http.StatusBadRequest))
	}
	if err := h.ensureRequestTask(request); err != nil {
		return fwcontext.NewResponse().Code(http.StatusServiceUnavailable).Header("Retry-After", "1").Content(http.StatusText(http.StatusServiceUnavailable))
	}
	if h.event != nil && h.event.HasListeners(event.EventHttpRun) {
		if err := h.event.DispatchProjectContext(requestEventContext(raw, false), event.NewHttpRunEvent()); err != nil {
			panic(err)
		}
	}

	response = h.middleware.Then(request, func(current *fwcontext.Request) *fwcontext.Response {
		// ThinkPHP 在全局中间件内进入路由分发；请求解析和静态资源适配均属于该目标阶段。
		if parseErr := current.Parse(); parseErr != nil {
			return responseForRequestParseError(parseErr)
		}
		if staticResponse := h.responseForPublicFile(current); staticResponse != nil {
			return staticResponse
		}
		if err := current.ApplyApplicationContext(); err != nil {
			h.logHTTPError("切换请求应用上下文失败", err)
			return fwcontext.NewResponse().Code(http.StatusBadRequest).Content(http.StatusText(http.StatusBadRequest))
		}
		if h.appMiddleware == nil {
			return h.routeRequest(current)
		}
		return h.appMiddleware.Then(current, h.routeRequest)
	})
	if response == nil {
		return internalServerErrorResponse()
	}
	if parseErr := request.ParseError(); parseErr != nil {
		return responseForRequestParseError(parseErr)
	}
	return response
}

// End 执行响应发送后的生命周期，对应 ThinkPHP Http::end。
// 响应已经提交，后续回调只能执行观测和资源清理，不能再改变客户端结果。
func (h *Http) End(response *fwcontext.Response) {
	if h == nil || response == nil {
		return
	}
	if h.applicationHost != nil && h.applicationHost.endAndReport(response) {
		return
	}
	state := h.takeRequestEndState(response)
	var request *fwcontext.Request
	if state != nil {
		request = state.request
	}
	endContext, cancelEnd := requestEndContext(request, h.srvConf.RequestEndTimeout)
	defer cancelEnd()
	if err := h.endRequestContext(endContext, response, state); err != nil {
		// 错误日志复用同一收尾预算；若事件已耗尽截止时间，RecordContext
		// 会立即返回并累计拒绝数，不能在 End 返回前再开启第二段等待。
		_ = h.logHTTPErrorContext(endContext, "HTTP 请求收尾失败", err)
	}
}

// endWithContext 让 ServeHTTP 的访问日志、HttpEnd、terminator 与资源清理共享
// 同一个整体截止时间；多应用委托也沿用该上下文，不能重新获得一段新预算。
func (h *Http) endWithContext(ctx stdcontext.Context, response *fwcontext.Response) error {
	if h == nil || response == nil {
		return nil
	}
	if h.applicationHost != nil {
		if handled, err := h.applicationHost.endWithContext(ctx, response); handled {
			return err
		}
	}
	return h.endRequestContext(ctx, response, h.takeRequestEndState(response))
}

func (h *Http) endRequestContext(
	endContext stdcontext.Context,
	response *fwcontext.Response,
	state *requestEndState,
) error {
	return runRequestEndStep(endContext, func() error {
		return h.runRequestEndLifecycle(endContext, response, state)
	})
}

// endRequestInline 在已确认没有可阻塞扩展时同步完成轻量收尾，避免普通请求创建监督协程。
func (h *Http) endRequestInline(response *fwcontext.Response) error {
	if h == nil || response == nil {
		return nil
	}
	return h.runRequestEndLifecycle(stdcontext.Background(), response, h.takeRequestEndState(response))
}

// runRequestEndLifecycle 在唯一监督任务中严格按 HttpEnd、terminate、cleanup 推进。
// endContext 只传递给扩展并约束外层等待，不能让当前步骤未返回时启动下一阶段。
func (h *Http) runRequestEndLifecycle(
	endContext stdcontext.Context,
	response *fwcontext.Response,
	state *requestEndState,
) error {
	if state != nil {
		defer state.task.Release()
	}
	var endErr error

	if h.event != nil && h.event.HasListeners(event.EventHttpEnd) {
		// 只有当前存在监听器时才创建事件，普通请求不承担未使用的载荷分配。
		endEvent := event.NewHttpEndEvent(response.GetStatus())
		endEvent.Data = response
		eventErr := runRequestEndStage(func() error {
			return h.event.DispatchTerminalSerialContext(endContext, endEvent)
		})
		endErr = errors.Join(endErr, wrapRequestEndError("HttpEnd 事件", eventErr))
	}
	var request *fwcontext.Request
	if state != nil {
		request = state.request
	}
	if state != nil && request != nil {
		endErr = errors.Join(endErr, h.runTerminators(endContext, request, response, state.terminators))
		cleanupErr := runRequestEndStage(func() error {
			return waitRequestCleanup(endContext, request)
		})
		endErr = errors.Join(endErr, wrapRequestEndError("请求资源清理", cleanupErr))
	}
	// 访问日志保持异步批处理；显式 Flush 仅作为调用方屏障，应用关停时
	// Log.Close 会可靠排空已接受条目，不能在每个请求上退化为 fsync。
	return endErr
}

func (h *Http) newDefaultRequest() (*fwcontext.Request, error) {
	raw, err := http.NewRequest(http.MethodGet, "http://localhost/", nil)
	if err != nil {
		return nil, err
	}
	scope, err := h.app.NewScope()
	if err != nil {
		return nil, err
	}
	request, err := h.newRequest(
		scope,
		raw,
		fwcontext.WithTrustedProxySet(h.srvConf.TrustedProxySet),
		fwcontext.WithMultipartMemoryLimit(h.srvConf.MultipartMemory),
		fwcontext.WithMaxBodyBytes(h.srvConf.MaxBodyBytes),
		fwcontext.WithEnvService(h.app.Env()),
		fwcontext.WithTrustedIdleServiceScope(scope, scope),
	)
	if err != nil {
		return nil, fmt.Errorf("创建请求上下文失败: %w", errors.Join(err, scope.Close()))
	}
	return request, nil
}

// newRequest 通过应用 request Provider 创建请求对象，并把当前请求作用域传入工厂。
func (h *Http) newRequest(scope *framework.ContainerScope, raw *http.Request, options ...fwcontext.RequestOption) (*fwcontext.Request, error) {
	if scope == nil {
		return nil, framework.ErrContainerScopeClosed
	}
	parameters := make([]interface{}, 0, len(options)+1)
	parameters = append(parameters, raw)
	for _, option := range options {
		parameters = append(parameters, option)
	}
	requestContext := stdcontext.Background()
	if raw != nil {
		requestContext = raw.Context()
	}
	created, err := scope.MakeContext(requestContext, string(framework.ServiceRequest), parameters...)
	if err != nil {
		return nil, fmt.Errorf("解析应用请求对象失败: %w", err)
	}
	request, ok := created.(*fwcontext.Request)
	if !ok || request == nil {
		return nil, fmt.Errorf("应用请求对象类型错误: %T", created)
	}
	return request, nil
}

func (h *Http) responseForPublicFile(request *fwcontext.Request) *fwcontext.Response {
	if request == nil {
		return nil
	}
	raw := request.Raw()
	writer, hasWriter := request.ResponseWriter()
	if raw == nil || raw.URL == nil || !hasWriter || (raw.Method != http.MethodGet && raw.Method != http.MethodHead) {
		return nil
	}
	staticPath := raw.URL.Path
	if staticPath == "/" {
		staticPath = "/index.html"
	}
	if !h.servePublicFile(writer, raw, staticPath) {
		return nil
	}
	return fwcontext.NewCommittedResponse(http.StatusOK)
}

func (h *Http) responseForRecoveredRequest(request *fwcontext.Request, recovered interface{}) *fwcontext.Response {
	if h == nil {
		return internalServerErrorResponse()
	}
	var raw *http.Request
	if request != nil {
		raw = request.Raw()
	}
	response, err := exception.RenderResponse(h.recoveredExceptionHandler(), raw, recovered)
	if err != nil {
		h.logHTTPError("异常渲染失败", err)
	}
	return response
}

func responseFromHTTPRecorder(recorder *httptest.ResponseRecorder) *fwcontext.Response {
	return exception.ResponseFromRecorder(recorder)
}

func isHostManagedHeader(name string) bool {
	return exception.IsHostManagedHeader(name)
}

func internalServerErrorResponse() *fwcontext.Response {
	return exception.InternalServerErrorResponse()
}

func (h *Http) rememberRequestEndState(response *fwcontext.Response, request *fwcontext.Request) {
	if h == nil || response == nil || request == nil {
		return
	}
	identity := response.Identity()
	if identity == nil {
		return
	}
	state := &requestEndState{
		request:     request,
		terminators: middleware.RequestTerminationCallbacks(request),
	}
	h.endStateMu.Lock()
	state.task = h.requestTasks[request].task
	delete(h.requestTasks, request)
	if h.endStates == nil {
		h.endStates = make(map[*fwcontext.ResponseIdentity]*requestEndState)
	}
	h.endStates[identity] = state
	h.endStateMu.Unlock()
}

func (h *Http) takeRequestEndState(response *fwcontext.Response) *requestEndState {
	if h == nil || response == nil {
		return nil
	}
	identity := response.Identity()
	if identity == nil {
		return nil
	}
	h.endStateMu.Lock()
	state := h.endStates[identity]
	delete(h.endStates, identity)
	h.endStateMu.Unlock()
	return state
}

func (h *Http) transferRequestEndState(previous, replacement *fwcontext.Response) {
	if h == nil || previous == nil || replacement == nil || previous == replacement {
		return
	}
	previousIdentity := previous.Identity()
	replacementIdentity := replacement.Identity()
	if previousIdentity == nil || replacementIdentity == nil || previousIdentity == replacementIdentity {
		return
	}
	h.endStateMu.Lock()
	state := h.endStates[previousIdentity]
	delete(h.endStates, previousIdentity)
	if state != nil {
		h.endStates[replacementIdentity] = state
	}
	h.endStateMu.Unlock()
}

func requestEndContext(request *fwcontext.Request, timeout time.Duration) (stdcontext.Context, stdcontext.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultRequestEndTimeout
	}
	base := stdcontext.Background()
	if request == nil {
		return stdcontext.WithTimeout(base, timeout)
	}
	base = stdcontext.WithoutCancel(request.Context())
	return stdcontext.WithTimeout(base, timeout)
}

// requestEndRequiresSupervisor 只要存在可能阻塞的事件、终结回调或请求资源，
// 就保留独立截止时间和后台监督；普通空闲请求走同步零分配路径。
func (h *Http) requestEndRequiresSupervisor(state *requestServeState) bool {
	if h != nil && h.event != nil && h.event.HasListeners(event.EventHttpEnd) {
		return true
	}
	if state == nil || state.req == nil {
		return false
	}
	if len(middleware.RequestTerminationCallbacks(state.req)) > 0 {
		return true
	}
	return state.req.CleanupRequiresSupervisor()
}

func runRequestEndStep(ctx stdcontext.Context, step func() error) (err error) {
	if step == nil {
		return nil
	}
	result := make(chan error, 1)
	go func() {
		var stepErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				stepErr = fmt.Errorf("请求收尾监督任务 panic: %v", recovered)
			}
			result <- stepErr
		}()
		stepErr = step()
	}()
	select {
	case err = <-result:
		return err
	case <-ctx.Done():
		select {
		case err = <-result:
			return err
		default:
			return ctx.Err()
		}
	}
}

func runRequestEndStage(stage func() error) (stageErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			stageErr = fmt.Errorf("请求收尾阶段 panic: %v", recovered)
		}
	}()
	if stage == nil {
		return nil
	}
	return stage()
}

func waitRequestCleanup(ctx stdcontext.Context, request *fwcontext.Request) error {
	cleanupErr := request.CleanupContext(ctx)
	if contextErr := ctx.Err(); contextErr != nil && errors.Is(cleanupErr, contextErr) {
		// CleanupContext 只让当前监督者结束等待；这里仍位于 HTTP 后台监督任务中，
		// 必须等待请求清理真实完成后才能宣告整条收尾链结束。
		cleanupErr = errors.Join(cleanupErr, request.Cleanup())
	}
	return cleanupErr
}

func wrapRequestEndError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s失败: %w", stage, err)
}
