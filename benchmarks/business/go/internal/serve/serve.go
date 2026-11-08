// Package serve 管理独立应用进程的配置、信号和资源关闭顺序。
package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
)

type Application interface {
	Run(context.Context) error
	Close() error
}

type Factory func(business.Config, *business.Service) (Application, error)

func Main(factory Factory) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, factory); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, factory Factory) error {
	configuration, err := business.LoadConfig()
	if err != nil {
		return err
	}
	service, err := business.Open(ctx, configuration)
	if err != nil {
		return err
	}
	application, err := factory(configuration, service)
	if err != nil {
		return errors.Join(err, service.Close())
	}
	return RunOwned(ctx, application, service)
}

// RunOwned 先排空HTTP请求，再关闭框架资源，最后释放数据库和缓存连接池。
func RunOwned(ctx context.Context, application Application, dependencies io.Closer) error {
	err := application.Run(ctx)
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		err = nil
	}
	return errors.Join(err, application.Close(), dependencies.Close())
}

// HTTP 只托管框架自身Handler，不提供任何绕过原生路由的业务处理。
func HTTP(ctx context.Context, address string, handler http.Handler, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return serveListener(ctx, listener, handler, timeout)
}

func serveListener(ctx context.Context, listener net.Listener, handler http.Handler, timeout time.Duration) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: timeout,
		ReadTimeout:       timeout,
		WriteTimeout:      timeout,
		IdleTimeout:       timeout,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			err = errors.Join(err, server.Close())
		}
		serveErr := <-result
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(err, serveErr)
	}
}
