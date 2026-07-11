package exception

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRenderDebugPageRejectsRemoteHTMLRequest 验证 debug 模式下也不会向远程 HTML 请求暴露源码调试页。
func TestRenderDebugPageRejectsRemoteHTMLRequest(t *testing.T) {
	handler := &Handle{
		App: &mockExceptionApp{debug: true},
		Log: &mockExceptionLogger{},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/debug", nil)
	req.RemoteAddr = "198.51.100.10:4567"
	recorder := httptest.NewRecorder()

	handler.Render(recorder, req, errors.New("debug boom"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("远程 HTML 请求应返回 500，实际为 %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "debug boom") {
		t.Fatalf("远程 HTML 请求不应暴露调试异常详情，实际响应为 %s", body)
	}
	if !strings.Contains(body, "Internal Server Error") {
		t.Fatalf("远程 HTML 请求应返回通用错误文案，实际响应为 %s", body)
	}
}

// TestRenderDebugPageRejectsLoopbackProxyRemoteClient 验证本机反代转发远程客户端时不会暴露调试页。
func TestRenderDebugPageRejectsLoopbackProxyRemoteClient(t *testing.T) {
	handler := &Handle{
		App: &mockExceptionApp{debug: true},
		Log: &mockExceptionLogger{},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/debug", nil)
	req.RemoteAddr = "127.0.0.1:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	recorder := httptest.NewRecorder()

	handler.Render(recorder, req, errors.New("debug boom"))

	body := recorder.Body.String()
	if strings.Contains(body, "debug boom") {
		t.Fatalf("本机反代后的远程请求不应暴露调试异常详情，实际响应为 %s", body)
	}
	if !strings.Contains(body, "Internal Server Error") {
		t.Fatalf("本机反代后的远程请求应返回通用错误文案，实际响应为 %s", body)
	}
}
