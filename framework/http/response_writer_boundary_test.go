package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPHostsRejectTypedNilResponseWriter 验证 HTTP 宿主遇到 typed nil 写入器时安全返回而不是 panic。
func TestHTTPHostsRejectTypedNilResponseWriter(t *testing.T) {
	var recorder *httptest.ResponseRecorder
	var writer stdhttp.ResponseWriter = recorder
	if !isNilHTTPResponseWriter(writer) {
		t.Fatal("typed nil ResponseWriter 应被识别为空")
	}

	httpHandler := &Http{}
	httpHandler.ServeHTTP(writer, httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil))
	(&MultiHttp{}).ServeHTTP(writer, httptest.NewRequest(stdhttp.MethodGet, "http://example.com", nil))
	httpHandler.ServeHTTP(nil, nil)
	(&MultiHttp{}).ServeHTTP(nil, nil)
}
