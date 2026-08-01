package http

import (
	stdcontext "context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"sync"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	frameworkLog "thinkgo/framework/log"
)

var (
	// ErrInvalidMultiHTTP 表示统一 HTTP 宿主缺少必要的应用或监听配置。
	ErrInvalidMultiHTTP = errors.New("统一 HTTP 宿主配置非法")
	// ErrMultiHTTPRunning 表示统一 HTTP 宿主已经在运行。
	ErrMultiHTTPRunning = errors.New("统一 HTTP 宿主正在运行")
)

// MultiHttp 在一个 HTTP/TLS/HTTP3 监听器后管理多个应用处理器。
type MultiHttp struct {
	manager  *framework.ApplicationManager
	resolver *framework.ApplicationResolver
	handlers map[string]*Http
	srvConf  serverConf

	runMu   sync.Mutex
	running bool
	runHost func() error
	// runHostContext 是可取消的宿主运行入口；runHost 保留测试与旧内部适配器语义。
	runHostContext func(stdcontext.Context) error
}

// multiHTTPKernel 将统一 HTTP 宿主函数适配为框架内核。
type multiHTTPKernel func() error

// Run 执行统一 HTTP 宿主函数。
func (kernel multiHTTPKernel) Run() error {
	return kernel()
}

// NewMultiHttp 创建统一 HTTP 宿主，并在监听前校验所有应用的共享服务器配置。
func NewMultiHttp(manager *framework.ApplicationManager) (*MultiHttp, error) {
	if manager == nil {
		return nil, fmt.Errorf("%w: 应用管理器不能为空", ErrInvalidMultiHTTP)
	}
	resolver, err := framework.NewApplicationResolver(manager)
	if err != nil {
		return nil, err
	}
	applications := manager.Applications()
	if len(applications) == 0 {
		return nil, fmt.Errorf("%w: 没有可用应用", ErrInvalidMultiHTTP)
	}

	names := make([]string, 0, len(applications))
	for name := range applications {
		names = append(names, name)
	}
	sort.Strings(names)
	handlers := make(map[string]*Http, len(names))
	var sharedServerConfig *serverConf
	for _, name := range names {
		application := applications[name]
		handler, handlerErr := NewHttp(application)
		if handlerErr != nil {
			return nil, fmt.Errorf("应用 %q 创建 HTTP 处理器失败: %w", name, handlerErr)
		}
		if sharedServerConfig == nil {
			configCopy := handler.srvConf
			sharedServerConfig = &configCopy
		} else if !reflect.DeepEqual(*sharedServerConfig, handler.srvConf) {
			return nil, fmt.Errorf("%w: 应用 %q 不能覆盖统一监听器配置", ErrInvalidMultiHTTP, name)
		}
		handlers[name] = handler
	}

	host := &MultiHttp{
		manager:  manager,
		resolver: resolver,
		handlers: handlers,
		srvConf:  *sharedServerConfig,
	}
	host.runHost = func() error {
		return runHTTPServers(host, host.srvConf, host.logHTTPError, host.logServerStarted)
	}
	host.runHostContext = func(ctx stdcontext.Context) error {
		return runHTTPServersContext(ctx, host, host.srvConf, host.logHTTPError, host.logServerStarted)
	}
	return host, nil
}

// ServeHTTP 解析应用并把克隆后的请求交给目标应用 HTTP 内核。
func (host *MultiHttp) ServeHTTP(writer http.ResponseWriter, raw *http.Request) {
	if isNilHTTPResponseWriter(writer) {
		return
	}
	if host == nil || host.manager == nil || host.resolver == nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if raw == nil || raw.URL == nil {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	resolution, err := host.resolver.Resolve(raw)
	if err != nil {
		host.writeResolutionError(writer, err)
		return
	}
	handler, exists := host.handlers[resolution.Name]
	if !exists || handler == nil {
		host.writeResolutionError(writer, fmt.Errorf("%w: 应用 %q 未创建 HTTP 处理器", framework.ErrApplicationNotFound, resolution.Name))
		return
	}
	cloned, err := cloneApplicationRequest(raw, resolution)
	if err != nil {
		host.writeResolutionError(writer, err)
		return
	}
	applicationContext := fwcontext.NewApplicationContextWithCanonicalHost(
		resolution.Name,
		resolution.OriginalPath,
		resolution.RewrittenPath,
		resolution.PathPrefix,
		raw.Host,
		resolution.CanonicalHost,
		resolution.DomainBound,
	)
	handler.serveHTTP(writer, cloned, &applicationContext)
}

// Run 启动一次统一监听器，并在退出时按管理器规则关闭所有应用。
func (host *MultiHttp) Run() error {
	return host.run(stdcontext.Background(), false)
}

// RunContext 启动一次统一 HTTP 宿主，并在上下文取消时完成优雅关闭。
func (host *MultiHttp) RunContext(ctx stdcontext.Context) error {
	if host == nil || host.manager == nil {
		return ErrInvalidMultiHTTP
	}
	if ctx == nil {
		return ErrInvalidRunContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return host.run(ctx, true)
}

func (host *MultiHttp) run(ctx stdcontext.Context, useContextHost bool) error {
	if host == nil || host.manager == nil {
		return ErrInvalidMultiHTTP
	}
	host.runMu.Lock()
	if host.running {
		host.runMu.Unlock()
		return ErrMultiHTTPRunning
	}
	host.running = true
	host.runMu.Unlock()
	defer func() {
		host.runMu.Lock()
		host.running = false
		host.runMu.Unlock()
	}()

	return host.manager.Run(multiHTTPKernel(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := host.freezeRoutes(); err != nil {
			return err
		}
		if useContextHost {
			if host.runHostContext == nil {
				return ErrInvalidMultiHTTP
			}
			return host.runHostContext(ctx)
		}
		if host.runHost == nil {
			return ErrInvalidMultiHTTP
		}
		return host.runHost()
	}))
}

func (host *MultiHttp) freezeRoutes() error {
	names := make([]string, 0, len(host.handlers))
	for name := range host.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		handler := host.handlers[name]
		if handler == nil || handler.app == nil || handler.route == nil {
			return fmt.Errorf("%w: 应用 %q 路由未初始化", ErrInvalidMultiHTTP, name)
		}
		if err := handler.ensureRouteFrozen(); err != nil {
			return fmt.Errorf("应用 %q 冻结路由失败: %w", name, err)
		}
	}
	return nil
}

func (host *MultiHttp) writeResolutionError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, framework.ErrInvalidApplicationRequest) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, framework.ErrApplicationNotFound) || errors.Is(err, framework.ErrApplicationDenied) {
		status = http.StatusNotFound
	}
	http.Error(writer, http.StatusText(status), status)
}

func (host *MultiHttp) logHTTPError(message string, err error) {
	if host == nil || host.manager == nil || err == nil {
		return
	}
	if application := host.manager.DefaultApplication(); application != nil {
		if logger, resolveErr := framework.ResolveServiceAs[*frameworkLog.Log](application, framework.ServiceLog); resolveErr == nil && logger != nil {
			logger.ErrorCtx(message, map[string]interface{}{"error": frameworkLog.SanitizeErrorText(err.Error())})
		}
	}
}

func (host *MultiHttp) logServerStarted(address string) {
	if host == nil || host.manager == nil {
		return
	}
	if application := host.manager.DefaultApplication(); application != nil {
		if logger, resolveErr := framework.ResolveServiceAs[*frameworkLog.Log](application, framework.ServiceLog); resolveErr == nil && logger != nil {
			logger.InfoCtx("统一 HTTP 服务已启动", map[string]interface{}{
				"address": address,
				"tls":     host.srvConf.EnableTLS,
				"http3":   host.srvConf.EnableHTTP3,
			})
		}
	}
}

func cloneApplicationRequest(raw *http.Request, resolution framework.ApplicationResolution) (*http.Request, error) {
	if raw == nil || raw.URL == nil {
		return nil, fmt.Errorf("%w: 原始请求不能为空", framework.ErrInvalidApplicationRequest)
	}
	cloned := raw.Clone(raw.Context())
	// Request.Clone 已经深拷贝 URL，这里只修改当前应用的重写路径，避免重复分配 URL。
	cloned.Body = raw.Body
	cloned.GetBody = nil
	cloned.URL.Path = resolution.RewrittenPath
	cloned.URL.RawPath = ""
	cloned.RequestURI = cloned.URL.RequestURI()
	return cloned, nil
}
