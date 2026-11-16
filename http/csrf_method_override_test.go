package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// TestCSRFProtectsOriginalAndEffectiveMethods 验证任意方法路由不会绕过 CSRF 校验。
func TestCSRFProtectsOriginalAndEffectiveMethods(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	config := middleware.DefaultCSRFConfig()
	csrf, err := middleware.CsrfWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err := mustHTTPRoute(t, app).Any("/csrf-method", func(request *fwcontext.Request) *fwcontext.Response {
		calls++
		return fwcontext.NewResponse().Content(request.Method())
	}, csrf); err != nil {
		t.Fatal(err)
	}
	host := newTestHTTPHandler(t, app)
	const endpoint = "http://127.0.0.1/csrf-method"
	page := httptest.NewRecorder()
	host.ServeHTTP(page, httptest.NewRequest(stdhttp.MethodGet, endpoint, nil))
	result := page.Result()
	defer result.Body.Close()
	var token *stdhttp.Cookie
	for _, cookie := range result.Cookies() {
		if cookie.Name == config.CookieName {
			token = cookie
		}
	}
	if page.Code != stdhttp.StatusOK || token == nil {
		t.Fatalf("安全请求未正常下发 CSRF Cookie: %d", page.Code)
	}
	for _, test := range []struct {
		name, original, override, effective string
		withToken                           bool
		want                                int
	}{
		{"POST 尝试覆写为 GET 仍须提供令牌", stdhttp.MethodPost, stdhttp.MethodGet, stdhttp.MethodPost, false, stdhttp.StatusForbidden},
		{"GET 尝试覆写为 PATCH 保持只读", stdhttp.MethodGet, stdhttp.MethodPatch, stdhttp.MethodGet, false, stdhttp.StatusOK},
		{"POST 覆写为 PATCH 仍须提供令牌", stdhttp.MethodPost, stdhttp.MethodPatch, stdhttp.MethodPatch, false, stdhttp.StatusForbidden},
		{"POST 提供令牌并覆写为 PATCH", stdhttp.MethodPost, stdhttp.MethodPatch, stdhttp.MethodPatch, true, stdhttp.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := calls
			req := httptest.NewRequest(test.original, endpoint, nil)
			req.Header.Set("X-HTTP-Method-Override", test.override)
			req.Header.Set("Origin", "http://127.0.0.1")
			req.AddCookie(token)
			if test.withToken {
				req.Header.Set(config.HeaderName, token.Value)
			}
			recorder := httptest.NewRecorder()
			host.ServeHTTP(recorder, req)
			if recorder.Code != test.want || test.want == stdhttp.StatusOK && (calls != before+1 || recorder.Body.String() != test.effective) || test.want == stdhttp.StatusForbidden && calls != before {
				t.Fatalf("状态=%d，预期=%d，动作调用次数变化=%d，响应=%q", recorder.Code, test.want, calls-before, recorder.Body.String())
			}
		})
	}
}
