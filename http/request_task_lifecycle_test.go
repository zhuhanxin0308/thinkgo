package http

import (
	stdcontext "context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestPendingRequestCleanupBoundsAdmissionAndAppClose 验证后台收尾真实结束前占用容量并保护应用依赖。
func TestPendingRequestCleanupBoundsAdmissionAndAppClose(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	configuration := mustHTTPConfig(t, app)
	for key, value := range map[string]interface{}{"request_end_timeout_ms": 20, "shutdown_timeout_ms": 20, "max_outstanding_requests": 1} {
		if err := configuration.Set("app.server."+key, value); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	defer close(release)
	mustHTTPMiddleware(t, app).PipeLifecycle(
		func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			return next(request)
		},
		func(*fwcontext.Request, *fwcontext.Response) { <-release },
	)
	if _, err := mustHTTPRoute(t, app).Get("/work", func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("ok") }); err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, app)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "http://example.com/work", nil))
	if first.Code != http.StatusOK || app.RequestTaskSnapshot().Active != 1 {
		t.Fatalf("超时收尾必须继续占用容量: status=%d tasks=%+v", first.Code, app.RequestTaskSnapshot())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "http://example.com/work", nil))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("积压时必须在业务执行前拒绝: %d", second.Code)
	}
	if err := app.Close(); !errors.Is(err, framework.ErrRequestTasksPending) {
		t.Fatalf("仍有收尾任务时不得关闭应用依赖: %v", err)
	}
	if _, err := app.ResolveService(framework.ServiceCache); err != nil {
		t.Fatalf("关闭超时后后台任务仍应能使用依赖: %v", err)
	}
}

// TestDirectRunEndReleasesRequestTask 验证嵌入式 Run/End 入口同样受任务登记保护。
func TestDirectRunEndReleasesRequestTask(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, app)
	response := handler.Run()
	if app.RequestTaskSnapshot().Active != 1 {
		t.Fatalf("Run 返回后必须等待 End 释放任务: %+v", app.RequestTaskSnapshot())
	}
	handler.End(response)
	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
	defer cancel()
	if err := app.WaitRequestTasks(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestDirectRunAbortReleasesRequestTask 验证无法返回响应的直接入口仍会完成清理。
func TestDirectRunAbortReleasesRequestTask(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	mustHTTPMiddleware(t, app).Pipe(func(*fwcontext.Request, func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		panic(http.ErrAbortHandler)
	})
	handler := newTestHTTPHandler(t, app)
	func() {
		defer func() {
			if recovered := recover(); recovered != http.ErrAbortHandler {
				t.Fatalf("直接入口必须保留宿主中断信号: %v", recovered)
			}
		}()
		handler.Run()
	}()
	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 100*time.Millisecond)
	defer cancel()
	if err := app.WaitRequestTasks(ctx); err != nil {
		t.Fatalf("无法返回 Response 的 Run 必须自行释放请求资源: %v", err)
	}
}
