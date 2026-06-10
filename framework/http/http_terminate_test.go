package http

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

// TestServeHTTPRunsTerminatorsAfterResponse 验证 terminate 会在响应发送后按执行顺序运行，
// 且 terminate 中对 Response 的修改不会反向污染已经写出的响应内容。
func TestServeHTTPRunsTerminatorsAfterResponse(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	order := make([]string, 0, 5)

	app.Middleware.PipeLifecycle(
		func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			order = append(order, "handle:global")
			return next(req)
		},
		func(req *fwcontext.Request, resp *fwcontext.Response) {
			order = append(order, "terminate:global")
			resp.Header("X-Terminate-Global", "1")
		},
	)

	routeMiddleware := middleware.Lifecycle(
		func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			order = append(order, "handle:route")
			return next(req)
		},
		func(req *fwcontext.Request, resp *fwcontext.Response) {
			order = append(order, "terminate:route")
			resp.Content("changed-after-send")
		},
	)

	app.Route.Get("/terminate", func(req *fwcontext.Request) *fwcontext.Response {
		order = append(order, "dispatch")
		return fwcontext.NewResponse().Content("ok")
	}, routeMiddleware)

	handler := NewHttp(app)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/terminate", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("响应状态码应为 200，实际为 %d", recorder.Code)
	}
	if body := recorder.Body.String(); body != "ok" {
		t.Fatalf("terminate 不应覆盖已经发送的响应体，实际为 %q", body)
	}
	if recorder.Header().Get("X-Terminate-Global") != "" {
		t.Fatal("terminate 在发送后执行时，不应再影响已经写出的响应头")
	}

	expectedOrder := []string{
		"handle:global",
		"handle:route",
		"dispatch",
		"terminate:global",
		"terminate:route",
	}
	if !reflect.DeepEqual(order, expectedOrder) {
		t.Fatalf("terminate 执行顺序错误，期望 %#v，实际为 %#v", expectedOrder, order)
	}
}
