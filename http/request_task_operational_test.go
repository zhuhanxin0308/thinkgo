package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestOperationalCapacityPreservesMiddlewareAndAccess 验证满载时仍可探测，但预留容量不能绕过来源和中间件。
func TestOperationalCapacityPreservesMiddlewareAndAccess(t *testing.T) {
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	configuration := `{"app_env":"test","operational_routes_enable":true,"server":{"host":"127.0.0.1","port":8080,"max_outstanding_requests":1,"request_end_timeout_ms":10},"compression":{"enable":false}}`
	if err := os.WriteFile(filepath.Join(basePath, "config", "app.json"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := framework.BuildConsoleApp(basePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	release := make(chan struct{})
	defer close(release)
	mustHTTPMiddleware(t, app).PipeLifecycle(
		func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			return next(request).Header("X-Global-Middleware", "executed")
		},
		func(*fwcontext.Request, *fwcontext.Response) { <-release },
	)
	if _, err := mustHTTPRoute(t, app).Get("/work", func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("ok") }); err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, app)
	serve := func(path, remote string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		request.RemoteAddr = remote
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := serve("/work", "127.0.0.1:1234"); response.Code != http.StatusOK {
		t.Fatalf("首个业务请求失败: %d", response.Code)
	}
	if response := serve("/work", "127.0.0.1:1234"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("业务容量必须独立封顶: %d", response.Code)
	}
	if response := serve(framework.OperationalLivenessPath, "127.0.0.1:1234"); response.Code != http.StatusOK || response.Header().Get("X-Global-Middleware") != "executed" {
		t.Fatalf("满载时健康探针仍须经过完整管线: status=%d headers=%v", response.Code, response.Header())
	}
	if response := serve(framework.OperationalLivenessPath, "203.0.113.1:1234"); response.Code != http.StatusNotFound || response.Header().Get("X-Global-Middleware") != "executed" {
		t.Fatalf("预留槽不能绕过运维来源限制: status=%d headers=%v", response.Code, response.Header())
	}
	// 运维自己的收尾也可能挂起，持续请求最终必须收到显式拒绝，不能无限创建监督协程。
	const maximumProbeAttempts = 64
	for attempt := 0; attempt < maximumProbeAttempts; attempt++ {
		if response := serve(framework.OperationalLivenessPath, "127.0.0.1:1234"); response.Code == http.StatusServiceUnavailable {
			snapshot := app.RequestTaskSnapshot()
			if snapshot.Operational == 0 || snapshot.Rejected < 2 || snapshot.Active != snapshot.Operational+1 {
				t.Fatalf("业务与运维容量统计错误: %+v", snapshot)
			}
			return
		}
	}
	t.Fatal("运维请求容量没有封顶")
}

// TestDisabledOperationalNameDoesNotReserveCapacity 验证普通同名业务路由不能借用运维容量。
func TestDisabledOperationalNameDoesNotReserveCapacity(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := mustHTTPConfig(t, app).Set("app.server.max_outstanding_requests", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := mustHTTPRoute(t, app).Get(framework.OperationalLivenessPath, func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("business") }); err != nil {
		t.Fatal(err)
	}
	task, err := app.AcquireRequestTask(1, defaultRequestEndTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Release()
	response := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.com"+framework.OperationalLivenessPath, nil))
	if response.Code != http.StatusServiceUnavailable || app.RequestTaskSnapshot().Operational != 0 {
		t.Fatalf("未启用的同名路由不得获得额外容量: status=%d tasks=%+v", response.Code, app.RequestTaskSnapshot())
	}
}
