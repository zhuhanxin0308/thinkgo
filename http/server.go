package http

import (
	stdcontext "context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/zhuhanxin0308/thinkgo/framework"
)

type serverResult struct {
	name string
	err  error
}

var (
	// ErrInvalidRunContext 表示 HTTP 服务运行上下文为空，无法安全管理服务生命周期。
	ErrInvalidRunContext = errors.New("HTTP 运行上下文无效")
	// ErrHTTPServerRunning 表示同一 HTTP 内核已经持有监听生命周期。
	ErrHTTPServerRunning = errors.New("HTTP 服务正在运行")
)

type applicationRunLeaseSet struct {
	leases []*framework.RunLease
	once   sync.Once
}

// Release 按获取的相反顺序释放子应用与根应用租约。
func (set *applicationRunLeaseSet) Release() {
	if set == nil {
		return
	}
	set.once.Do(func() {
		for index := len(set.leases) - 1; index >= 0; index-- {
			set.leases[index].Release()
		}
	})
}

// Server 是 HTTP 监听宿主，实现 framework.Kernel 所需的无参 Run。
// 请求执行仍由 Http.Run(Request) 负责，业务代码无需接触端口和信号处理。
type Server struct {
	http *Http
}

// NewServer 创建绑定指定 Http 请求内核的监听宿主。
func NewServer(httpKernel *Http) *Server {
	return &Server{http: httpKernel}
}

// Run 启动监听宿主，供 App.Kernel 和命令行入口调用。
func (server *Server) Run() error {
	if server == nil || server.http == nil {
		return errors.New("HTTP 监听宿主未初始化")
	}
	return server.http.Listen()
}

// RunContext 允许嵌入式宿主通过上下文控制监听生命周期。
func (server *Server) RunContext(ctx stdcontext.Context) error {
	if server == nil || server.http == nil {
		return errors.New("HTTP 监听宿主未初始化")
	}
	return server.http.ListenContext(ctx)
}

// Listen 启动项目 HTTP/TLS/HTTP3 服务，并统一处理信号、服务错误和优雅关闭结果。
func (h *Http) Listen() error {
	return h.ListenContext(stdcontext.Background())
}

// ListenContext 启动 HTTP/TLS/HTTP3 服务，并在上下文取消或进程收到终止信号时优雅退出。
func (h *Http) ListenContext(ctx stdcontext.Context) error {
	if h == nil || h.app == nil {
		return errors.New("HTTP 内核未初始化")
	}
	if ctx == nil {
		return ErrInvalidRunContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.beginListening(); err != nil {
		return err
	}
	defer h.endListening()
	if err := h.ensureInitialized(); err != nil {
		return err
	}
	if h.route == nil {
		return errors.New("HTTP 路由服务未初始化")
	}
	if err := validateServerTLS(h.srvConf); err != nil {
		return err
	}
	leases, err := h.acquireApplicationRunLeases()
	if err != nil {
		return err
	}
	defer leases.Release()
	if err := h.reloadApplicationRuntimeServices(); err != nil {
		return err
	}
	runErr := runHTTPServersContext(ctx, h, h.srvConf, h.logHTTPError, h.logServerStarted)
	return errors.Join(runErr, h.waitApplicationRequestTasks())
}

func (h *Http) beginListening() error {
	h.listenMu.Lock()
	defer h.listenMu.Unlock()
	if h.listening {
		return ErrHTTPServerRunning
	}
	h.listening = true
	return nil
}

func (h *Http) endListening() {
	if h == nil {
		return
	}
	h.listenMu.Lock()
	h.listening = false
	h.listenMu.Unlock()
}

// acquireApplicationRunLeases 同时冻结根应用和原生子应用的运行期服务快照。
func (h *Http) acquireApplicationRunLeases() (*applicationRunLeaseSet, error) {
	if h == nil || h.app == nil {
		return nil, errors.New("HTTP 内核未初始化")
	}
	handlers := h.applicationLifecycleHandlers()
	applications := make([]*framework.App, 0, len(handlers))
	seen := make(map[*framework.App]struct{}, len(handlers))
	for _, handler := range handlers {
		if handler == nil || handler.app == nil {
			continue
		}
		if _, exists := seen[handler.app]; exists {
			continue
		}
		seen[handler.app] = struct{}{}
		applications = append(applications, handler.app)
	}
	set := &applicationRunLeaseSet{leases: make([]*framework.RunLease, 0, len(applications))}
	for _, application := range applications {
		lease, err := application.AcquireRunLease()
		if err != nil {
			set.Release()
			return nil, fmt.Errorf("获取 HTTP 应用运行租约失败: %w", err)
		}
		set.leases = append(set.leases, lease)
	}
	return set, nil
}

// reloadApplicationRuntimeServices 在所有根/子应用都进入 Running 后
// 发布容器中的最终服务快照，从而闭合 ensureInitialized 与租约之间的变更窗口。
func (h *Http) reloadApplicationRuntimeServices() error {
	for _, handler := range h.applicationLifecycleHandlers() {
		if err := handler.reloadRuntimeServices(); err != nil {
			name := handler.GetName()
			if name == "" {
				name = "root"
			}
			return fmt.Errorf("刷新 HTTP 应用 %q 最终服务快照失败: %w", name, err)
		}
	}
	return nil
}

func (h *Http) applicationLifecycleHandlers() []*Http {
	if h == nil {
		return nil
	}
	candidates := []*Http{h}
	if h.applicationHost != nil {
		candidates = append(candidates, h.applicationHost.lifecycleHandlers()...)
	}
	handlers := make([]*Http, 0, len(candidates))
	seen := make(map[*Http]struct{}, len(candidates))
	for _, handler := range candidates {
		if handler == nil {
			continue
		}
		if _, exists := seen[handler]; exists {
			continue
		}
		seen[handler] = struct{}{}
		handlers = append(handlers, handler)
	}
	return handlers
}

// runHTTPServers 在单个 TCP 监听器后运行 HTTP/TLS 和可选的 HTTP/3 服务。
func runHTTPServers(
	handler http.Handler,
	serverConfig serverConf,
	logHTTPError func(string, error),
	logServerStarted func(string),
) error {
	return runHTTPServersContext(stdcontext.Background(), handler, serverConfig, logHTTPError, logServerStarted)
}

// runHTTPServersContext 在统一的上下文和终止信号下运行 HTTP 与 HTTP/3 服务。
func runHTTPServersContext(
	ctx stdcontext.Context,
	handler http.Handler,
	serverConfig serverConf,
	logHTTPError func(string, error),
	logServerStarted func(string),
) error {
	if ctx == nil {
		return ErrInvalidRunContext
	}
	if handler == nil {
		return errors.New("HTTP 处理器未初始化")
	}
	if err := validateServerTLS(serverConfig); err != nil {
		return err
	}
	runContext, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	tcpServer := newConfiguredHTTPServer(handler, serverConfig, logHTTPError)
	tcpListener, err := net.Listen("tcp", tcpServer.Addr)
	if err != nil {
		return fmt.Errorf("HTTP 服务异常退出: TCP 监听失败: %w", err)
	}
	// 使用实际监听地址更新 HTTP/3 配置，尤其要覆盖端口为 0 时的动态端口。
	tcpServer.Addr = tcpListener.Addr().String()
	var http3Server *boundedHTTP3Server
	var http3PacketConn net.PacketConn
	resultChannel := make(chan serverResult, 2)
	serverCount := 1
	go func() {
		var err error
		if serverConfig.EnableTLS {
			err = tcpServer.ServeTLS(tcpListener, serverConfig.CertFile, serverConfig.KeyFile)
		} else {
			err = tcpServer.Serve(tcpListener)
		}
		resultChannel <- serverResult{name: "HTTP", err: normalizeServerError(err)}
	}()

	if serverConfig.EnableHTTP3 {
		serverCount++
		protocolServer := newConfiguredHTTP3Server(handler, serverConfig, tcpServer.Addr)
		certificate, err := tls.LoadX509KeyPair(serverConfig.CertFile, serverConfig.KeyFile)
		if err != nil {
			_ = tcpListener.Close()
			return errors.Join(fmt.Errorf("HTTP/3 TLS 证书加载失败: %w", err), tcpServer.Close())
		}
		protocolServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
		http3Server = newBoundedHTTP3Server(protocolServer, serverConfig)
		http3PacketConn, err = net.ListenPacket("udp", tcpServer.Addr)
		if err != nil {
			_ = tcpListener.Close()
			return errors.Join(fmt.Errorf("HTTP/3 服务异常退出: UDP 监听失败: %w", err), tcpServer.Close(), http3Server.Close())
		}
		go func() {
			err := http3Server.Serve(http3PacketConn)
			resultChannel <- serverResult{name: "HTTP/3", err: normalizeServerError(err)}
		}()
	}
	if logServerStarted != nil {
		logServerStarted(tcpServer.Addr)
	}

	var runErr error
	completed := 0
	select {
	case result := <-resultChannel:
		completed++
		if result.err != nil {
			runErr = fmt.Errorf("%s 服务异常退出: %w", result.name, result.err)
		}
	case <-runContext.Done():
		// 进程信号表示正常停机；调用方上下文取消需要保留原因，便于编排器判断退出状态。
		if ctxErr := ctx.Err(); ctxErr != nil {
			runErr = ctxErr
		}
	}

	// 先关闭底层监听器，避免 Serve 尚未完成内部注册时 Shutdown 遗留监听句柄。
	if err := tcpListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		runErr = errors.Join(runErr, fmt.Errorf("关闭 TCP 监听器失败: %w", err))
	}
	shutdownErr := shutdownConfiguredServers(tcpServer, http3Server, serverConfig.ShutdownTimeout)
	// UDP 承载已有 QUIC 会话，只能在排空完成或强制关闭后释放。
	if http3PacketConn != nil {
		if err := http3PacketConn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			runErr = errors.Join(runErr, fmt.Errorf("关闭 HTTP/3 UDP 监听器失败: %w", err))
		}
	}
	deadline := time.NewTimer(serverConfig.ShutdownTimeout)
	defer deadline.Stop()
	for completed < serverCount {
		select {
		case result := <-resultChannel:
			completed++
			if result.err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("%s 服务退出失败: %w", result.name, result.err))
			}
		case <-deadline.C:
			runErr = errors.Join(runErr, errors.New("等待 HTTP 服务退出超时"))
			completed = serverCount
		}
	}
	return errors.Join(runErr, shutdownErr)
}

func validateServerTLS(serverConfig serverConf) error {
	if !serverConfig.EnableTLS {
		return nil
	}
	if _, err := tls.LoadX509KeyPair(serverConfig.CertFile, serverConfig.KeyFile); err != nil {
		return fmt.Errorf("加载 TLS 证书失败: %w", err)
	}
	return nil
}

// newHTTP3Server 保留项目级测试和扩展点，并让 HTTP/3 复用统一配置。
func (h *Http) newHTTP3Server(address string) *http3.Server {
	if h == nil {
		return nil
	}
	return newConfiguredHTTP3Server(h, h.srvConf, address)
}

func newConfiguredHTTP3Server(handler http.Handler, serverConfig serverConf, address string) *http3.Server {
	return &http3.Server{
		Addr:           address,
		Handler:        handler,
		MaxHeaderBytes: serverConfig.MaxHeaderBytes,
		IdleTimeout:    serverConfig.IdleTimeout,
		TLSConfig:      &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func (h *Http) newServer() *http.Server {
	if h == nil {
		return nil
	}
	return newConfiguredHTTPServer(h, h.srvConf, h.logHTTPError)
}

func newConfiguredHTTPServer(handler http.Handler, serverConfig serverConf, logHTTPError func(string, error)) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort(serverConfig.Host, fmt.Sprintf("%d", serverConfig.Port)),
		Handler:           handler,
		ReadHeaderTimeout: serverConfig.ReadHeaderTimeout,
		ReadTimeout:       serverConfig.ReadTimeout,
		WriteTimeout:      serverConfig.WriteTimeout,
		IdleTimeout:       serverConfig.IdleTimeout,
		MaxHeaderBytes:    serverConfig.MaxHeaderBytes,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		ErrorLog:          stdlog.New(&serverErrorLogWriter{onError: logHTTPError}, "", 0),
	}
}

type serverErrorLogWriter struct {
	http    *Http
	onError func(string, error)
}

func (w *serverErrorLogWriter) Write(message []byte) (int, error) {
	if w != nil {
		err := errors.New(strings.TrimSpace(string(message)))
		if w.http != nil {
			w.http.logHTTPError("HTTP Server 错误", err)
		} else if w.onError != nil {
			w.onError("HTTP Server 错误", err)
		}
	}
	return len(message), nil
}

func (h *Http) shutdownServers(tcpServer *http.Server, http3Server *http3.Server) error {
	if h == nil {
		return errors.New("HTTP 内核未初始化")
	}
	return shutdownConfiguredServers(tcpServer, http3Server, h.srvConf.ShutdownTimeout)
}

func shutdownConfiguredServers(tcpServer *http.Server, http3Server http3ShutdownServer, timeout time.Duration) error {
	if http3Server != nil {
		value := reflect.ValueOf(http3Server)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			http3Server = nil
		}
	}
	shutdownContext, cancel := stdcontext.WithTimeout(stdcontext.Background(), timeout)
	defer cancel()
	results := make(chan error, 2)
	serverCount := 0
	if tcpServer != nil {
		serverCount++
		go func() {
			err := tcpServer.Shutdown(shutdownContext)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				results <- errors.Join(fmt.Errorf("关闭 HTTP 服务失败: %w", err), tcpServer.Close())
				return
			}
			results <- nil
		}()
	}
	if http3Server != nil {
		serverCount++
		go func() {
			err := http3Server.Shutdown(shutdownContext)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				results <- errors.Join(fmt.Errorf("关闭 HTTP/3 服务失败: %w", err), http3Server.Close())
				return
			}
			results <- nil
		}()
	}
	var shutdownErr error
	for index := 0; index < serverCount; index++ {
		shutdownErr = errors.Join(shutdownErr, <-results)
	}
	return shutdownErr
}

func normalizeServerError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (h *Http) logServerStarted(address string) {
	if h == nil {
		return
	}
	writeServerListeningAddress(h.startupOutput, address, h.srvConf.EnableTLS)
	if h.log == nil {
		return
	}
	h.log.InfoCtx("HTTP 服务已启动", map[string]interface{}{
		"address": address,
		"tls":     h.srvConf.EnableTLS,
		"http3":   h.srvConf.EnableHTTP3,
	})
}

// writeServerListeningAddress 在真实监听器绑定成功后输出可直接识别的服务地址。
func writeServerListeningAddress(output io.Writer, address string, enableTLS bool) {
	if output == nil {
		return
	}
	scheme := "http"
	if enableTLS {
		scheme = "https"
	}
	_, _ = fmt.Fprintf(output, "Server listening on %s://%s\n", scheme, address)
}
