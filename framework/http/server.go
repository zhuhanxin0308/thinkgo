package http

import (
	stdcontext "context"
	"crypto/tls"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/quic-go/quic-go/http3"
)

type serverResult struct {
	name string
	err  error
}

// Run 启动 HTTP/TLS/HTTP3 服务，并统一处理信号、服务错误和优雅关闭结果。
func (h *Http) Run() error {
	if h == nil || h.app == nil {
		return errors.New("HTTP 内核未初始化")
	}
	if err := h.app.Route.Freeze(); err != nil {
		return fmt.Errorf("冻结路由失败: %w", err)
	}
	if h.srvConf.EnableTLS {
		if _, err := tls.LoadX509KeyPair(h.srvConf.CertFile, h.srvConf.KeyFile); err != nil {
			return fmt.Errorf("加载 TLS 证书失败: %w", err)
		}
	}

	tcpServer := h.newServer()
	var http3Server *http3.Server
	resultChannel := make(chan serverResult, 2)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	serverCount := 1
	go func() {
		var err error
		if h.srvConf.EnableTLS {
			err = tcpServer.ListenAndServeTLS(h.srvConf.CertFile, h.srvConf.KeyFile)
		} else {
			err = tcpServer.ListenAndServe()
		}
		resultChannel <- serverResult{name: "HTTP", err: normalizeServerError(err)}
	}()

	if h.srvConf.EnableHTTP3 {
		serverCount++
		http3Server = h.newHTTP3Server(tcpServer.Addr)
		go func() {
			err := http3Server.ListenAndServeTLS(h.srvConf.CertFile, h.srvConf.KeyFile)
			resultChannel <- serverResult{name: "HTTP/3", err: normalizeServerError(err)}
		}()
	}
	h.logServerStarted(tcpServer.Addr)

	var runErr error
	completed := 0
	select {
	case result := <-resultChannel:
		completed++
		if result.err != nil {
			runErr = fmt.Errorf("%s 服务异常退出: %w", result.name, result.err)
		}
	case <-signals:
	}

	shutdownErr := h.shutdownServers(tcpServer, http3Server)
	deadline := time.NewTimer(h.srvConf.ShutdownTimeout)
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

// newHTTP3Server 让 HTTP/3 与 TCP 服务共享请求头和空闲连接安全边界。
func (h *Http) newHTTP3Server(address string) *http3.Server {
	return &http3.Server{
		Addr:           address,
		Handler:        h,
		MaxHeaderBytes: h.srvConf.MaxHeaderBytes,
		IdleTimeout:    h.srvConf.IdleTimeout,
	}
}

func (h *Http) newServer() *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort(h.srvConf.Host, fmt.Sprintf("%d", h.srvConf.Port)),
		Handler:           h,
		ReadHeaderTimeout: h.srvConf.ReadHeaderTimeout,
		ReadTimeout:       h.srvConf.ReadTimeout,
		WriteTimeout:      h.srvConf.WriteTimeout,
		IdleTimeout:       h.srvConf.IdleTimeout,
		MaxHeaderBytes:    h.srvConf.MaxHeaderBytes,
		ErrorLog:          stdlog.New(&serverErrorLogWriter{http: h}, "", 0),
	}
}

type serverErrorLogWriter struct {
	http *Http
}

func (w *serverErrorLogWriter) Write(message []byte) (int, error) {
	if w != nil && w.http != nil {
		w.http.logHTTPError("HTTP Server 错误", errors.New(strings.TrimSpace(string(message))))
	}
	return len(message), nil
}

func (h *Http) shutdownServers(tcpServer *http.Server, http3Server *http3.Server) error {
	shutdownContext, cancel := stdcontext.WithTimeout(stdcontext.Background(), h.srvConf.ShutdownTimeout)
	defer cancel()
	results := make(chan error, 2)
	serverCount := 0
	if tcpServer != nil {
		serverCount++
		go func() {
			err := tcpServer.Shutdown(shutdownContext)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				results <- fmt.Errorf("关闭 HTTP 服务失败: %w", err)
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
				results <- fmt.Errorf("关闭 HTTP/3 服务失败: %w", err)
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
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (h *Http) logServerStarted(address string) {
	if h.app.Log == nil {
		return
	}
	h.app.Log.InfoCtx("HTTP 服务已启动", map[string]interface{}{
		"address": address,
		"tls":     h.srvConf.EnableTLS,
		"http3":   h.srvConf.EnableHTTP3,
	})
}
