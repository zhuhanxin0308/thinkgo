package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	_ "thinkgo/app/controller"
	_ "thinkgo/app/middleware"
	appmiddleware "thinkgo/app/middleware"
	"thinkgo/framework"
	fwhttp "thinkgo/framework/http"
	_ "thinkgo/route"
)

// TestApplicationRegistersControllerMiddlewareRouteAndRequestID 验证应用入口注册了控制器、中间件和路由。
func TestApplicationRegistersControllerMiddlewareRouteAndRequestID(t *testing.T) {
	app := framework.NewConsoleApp()
	t.Cleanup(func() { _ = app.Close() })
	handler, err := fwhttp.NewHttp(app)
	if err != nil {
		t.Fatalf("创建 HTTP 内核失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/users", nil)
	req.Header.Set("X-Request-ID", "test-request-id")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("/api/users 应返回 200，实际为 %d，body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != "User List" {
		t.Fatalf("/api/users 应由 User 控制器响应，实际为 %q", recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Request-ID"); got != "test-request-id" {
		t.Fatalf("RequestID 中间件应透传请求编号，实际为 %q", got)
	}
	if appmiddleware.RequestIDKey == "" {
		t.Fatal("RequestIDKey 不应为空")
	}
}
