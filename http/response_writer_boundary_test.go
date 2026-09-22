package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPRejectsTypedNilResponseWriter 验证 HTTP 内核遇到 typed nil 写入器时安全返回而不是 panic。
func TestHTTPRejectsTypedNilResponseWriter(t *testing.T) {
	var recorder *httptest.ResponseRecorder
	var writer stdhttp.ResponseWriter = recorder
	if !isNilHTTPResponseWriter(writer) {
		t.Fatal("typed nil ResponseWriter 应被识别为空")
	}

	httpHandler := &Http{}
	httpHandler.ServeHTTP(writer, httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil))
	httpHandler.ServeHTTP(nil, nil)
}

// TestNilHTTPHandlerCompletesObservabilityDeferSafely 验证 nil 内核返回 500 后，
// 请求完成阶段不会因为读取遥测服务再次 panic。
func TestNilHTTPHandlerCompletesObservabilityDeferSafely(t *testing.T) {
	var handler *Http
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil))
	if recorder.Code != stdhttp.StatusInternalServerError {
		t.Fatalf("nil HTTP 内核应返回 500，实际为 %d", recorder.Code)
	}
}
