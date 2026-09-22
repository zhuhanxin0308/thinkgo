package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

type requestTaskAddressWriter struct {
	address chan string
}

func (writer *requestTaskAddressWriter) Write(value []byte) (int, error) {
	writer.address <- strings.TrimSpace(strings.TrimPrefix(string(value), "Server listening on "))
	return len(value), nil
}

// TestListeningHostWaitsForRequestCleanup 验证响应已完整发送后，监听宿主仍等待真实收尾并报告预算耗尽。
func TestListeningHostWaitsForRequestCleanup(t *testing.T) {
	for _, exceedsBudget := range []bool{false, true} {
		name := "completed"
		if exceedsBudget {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			release := make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			mustHTTPMiddleware(t, app).PipeLifecycle(
				func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
					return next(request)
				},
				func(*fwcontext.Request, *fwcontext.Response) { <-release },
			)
			if _, err := mustHTTPRoute(t, app).Get("/work", func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("complete") }); err != nil {
				t.Fatal(err)
			}
			handler := newTestHTTPHandler(t, app)
			if err := handler.ensureInitialized(); err != nil {
				t.Fatal(err)
			}
			handler.srvConf.Port = 0
			handler.srvConf.RequestEndTimeout = 10 * time.Millisecond
			handler.srvConf.ShutdownTimeout = 150 * time.Millisecond
			addresses := make(chan string, 1)
			handler.startupOutput = &requestTaskAddressWriter{address: addresses}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			completed := make(chan struct{})
			go func() {
				defer close(completed)
				done <- handler.ListenContext(ctx)
			}()
			t.Cleanup(func() {
				finish()
				cancel()
				select {
				case <-completed:
				case <-time.After(protocolTestTimeout):
					t.Error("监听宿主测试遗留服务资源")
				}
			})
			var address string
			select {
			case address = <-addresses:
			case err := <-done:
				t.Fatalf("监听启动失败: %v", err)
			case <-time.After(protocolTestTimeout):
				t.Fatal("监听启动超时")
			}
			client := &http.Client{Timeout: protocolTestTimeout}
			response, err := client.Get(address + "/work")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			client.CloseIdleConnections()
			if err != nil || string(body) != "complete" {
				t.Fatalf("收尾等待前响应必须已经完整到达: %q %v", body, err)
			}
			cancel()
			select {
			case err := <-done:
				t.Fatalf("后台清理尚未完成时宿主提前退出: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			if !exceedsBudget {
				finish()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || errors.Is(err, framework.ErrRequestTasksPending) != exceedsBudget {
					t.Fatalf("监听退出必须准确报告收尾状态: %v", err)
				}
			case <-time.After(protocolTestTimeout):
				t.Fatal("监听退出没有遵守收尾预算")
			}
			if exceedsBudget {
				if _, err := app.ResolveService(framework.ServiceCache); err != nil || app.RequestTaskSnapshot().Active != 1 {
					t.Fatalf("超时后清理仍须保有依赖与租约: %v %+v", err, app.RequestTaskSnapshot())
				}
			}
			finish()
			waitContext, stop := context.WithTimeout(context.Background(), protocolTestTimeout)
			defer stop()
			if err := app.WaitRequestTasks(waitContext); err != nil {
				t.Fatal(err)
			}
		})
	}
}
