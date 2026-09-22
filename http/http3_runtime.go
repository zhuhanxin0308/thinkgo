package http

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// http3ShutdownServer 让监听宿主以相同的关闭契约管理标准库与有逐流期限的 HTTP/3 运行器。
type http3ShutdownServer interface {
	Shutdown(context.Context) error
	Close() error
}

// boundedHTTP3Server 只负责 QUIC 生命周期和截止时间，HTTP/3 解析、校验与响应仍由成熟依赖执行。
// RawServerConn 没有公开 GOAWAY 接口，因此排空阶段拒绝新请求，并等待客户端关闭已有连接。
type boundedHTTP3Server struct {
	server   *http3.Server
	config   serverConf
	mu       sync.Mutex
	listener *quic.EarlyListener
	conns    map[*quic.Conn]struct{}
	idle     chan struct{}
	closed   bool
	draining atomic.Bool
}

func newBoundedHTTP3Server(server *http3.Server, config serverConf) *boundedHTTP3Server {
	server.Handler = http3DeadlineHandler(server.Handler, config)
	return &boundedHTTP3Server{server: server, config: config, conns: make(map[*quic.Conn]struct{})}
}

func (server *boundedHTTP3Server) Serve(packetConn net.PacketConn) error {
	listener, err := quic.ListenEarly(packetConn, http3.ConfigureTLSConfig(server.server.TLSConfig), server.server.QUICConfig)
	if err != nil {
		return err
	}
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		_ = listener.Close()
		return http.ErrServerClosed
	}
	server.listener = listener
	server.mu.Unlock()
	for {
		connection, err := listener.Accept(context.Background())
		if err != nil {
			if server.draining.Load() {
				return http.ErrServerClosed
			}
			return err
		}
		server.mu.Lock()
		if server.closed {
			server.mu.Unlock()
			_ = connection.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeRequestRejected), "server shutting down")
			continue
		}
		server.conns[connection] = struct{}{}
		server.mu.Unlock()
		go server.serveConnection(connection)
	}
}

func (server *boundedHTTP3Server) serveConnection(connection *quic.Conn) {
	defer func() {
		_ = connection.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "")
		server.mu.Lock()
		delete(server.conns, connection)
		if len(server.conns) == 0 && server.idle != nil {
			close(server.idle)
			server.idle = nil
		}
		server.mu.Unlock()
	}()
	raw, err := server.server.NewRawServerConn(connection)
	if err != nil {
		return
	}
	var streams sync.WaitGroup
	streams.Add(1)
	go func() {
		defer streams.Done()
		for {
			stream, err := connection.AcceptUniStream(connection.Context())
			if err != nil {
				return
			}
			streams.Add(1)
			go func() {
				defer streams.Done()
				raw.HandleUnidirectionalStream(stream)
			}()
		}
	}()
	for {
		stream, err := connection.AcceptStream(connection.Context())
		if err != nil {
			break
		}
		if server.draining.Load() {
			stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
			stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
			continue
		}
		// 请求头尚未进入 Handler，必须在交给 HTTP/3 解析器之前设置流读取期限。
		if server.config.ReadHeaderTimeout > 0 {
			_ = stream.SetReadDeadline(time.Now().Add(server.config.ReadHeaderTimeout))
		}
		streams.Add(1)
		go func() {
			defer streams.Done()
			raw.HandleRequestStream(stream)
		}()
	}
	streams.Wait()
}

// Shutdown 保留底层 UDP 及在途响应；没有 GOAWAY 时客户端不关闭连接也必须有明确超时结果。
func (server *boundedHTTP3Server) Shutdown(ctx context.Context) error {
	server.mu.Lock()
	server.closed = true
	server.draining.Store(true)
	listener := server.listener
	if len(server.conns) == 0 {
		server.mu.Unlock()
		if listener != nil {
			return listener.Close()
		}
		return nil
	}
	if server.idle == nil {
		server.idle = make(chan struct{})
	}
	idle := server.idle
	server.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return errors.Join(ctx.Err(), server.Close())
	}
}

// Close 发出强制关闭并立即返回，不等待可能拒绝取消的业务回调。
func (server *boundedHTTP3Server) Close() error {
	server.mu.Lock()
	server.closed = true
	server.draining.Store(true)
	listener := server.listener
	connections := make([]*quic.Conn, 0, len(server.conns))
	for connection := range server.conns {
		connections = append(connections, connection)
	}
	server.mu.Unlock()
	var closeErr error
	if listener != nil {
		closeErr = listener.Close()
	}
	for _, connection := range connections {
		closeErr = errors.Join(closeErr, connection.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "server closed"))
	}
	return closeErr
}

// http3DeadlineHandler 在头部解析完成后把读取期限切换为 body 预算，并为响应设置写入期限。
func http3DeadlineHandler(next http.Handler, config serverConf) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		controller := http.NewResponseController(writer)
		if config.ReadTimeout > 0 {
			_ = controller.SetReadDeadline(time.Now().Add(config.ReadTimeout))
		}
		if config.WriteTimeout > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(config.WriteTimeout))
		}
		next.ServeHTTP(writer, request)
	})
}
