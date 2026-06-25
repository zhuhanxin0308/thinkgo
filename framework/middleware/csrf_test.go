package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "thinkgo/framework/context"
)

// TestCsrfIssuesTokenOnSafeMethod 验证安全方法会下发 CSRF token Cookie。
func TestCsrfIssuesTokenOnSafeMethod(t *testing.T) {
	handler := Csrf()
	req := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))

	resp := handler(req, func(r *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	})

	setCookie := resp.Headers().Get("Set-Cookie")
	if !strings.Contains(setCookie, DefaultCSRFCookieName+"=") {
		t.Fatalf("安全方法应下发 CSRF token Cookie，实际 Set-Cookie=%q", setCookie)
	}
	if strings.Contains(strings.ToLower(setCookie), "httponly") {
		t.Fatalf("CSRF Cookie 不应为 HttpOnly（双重提交需前端可读），实际 %q", setCookie)
	}
}

// TestCsrfBlocksUnsafeWithoutToken 验证缺少 token 的状态变更请求被拦截。
func TestCsrfBlocksUnsafeWithoutToken(t *testing.T) {
	handler := Csrf()
	req := fwcontext.NewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/save", nil))

	called := false
	resp := handler(req, func(r *fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("done")
	})

	if called {
		t.Fatal("缺少 CSRF token 的 POST 不应进入业务处理")
	}
	if resp.GetStatus() != http.StatusForbidden {
		t.Fatalf("缺少 CSRF token 应返回 403，实际 %d", resp.GetStatus())
	}
}

// TestCsrfBlocksMismatchedToken 验证 Cookie 与请求头 token 不一致时被拦截。
func TestCsrfBlocksMismatchedToken(t *testing.T) {
	handler := Csrf()
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/save", nil)
	raw.AddCookie(&http.Cookie{Name: DefaultCSRFCookieName, Value: "cookie-token"})
	raw.Header.Set(DefaultCSRFHeaderName, "different-token")
	req := fwcontext.NewRequest(raw)

	resp := handler(req, func(r *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("done")
	})
	if resp.GetStatus() != http.StatusForbidden {
		t.Fatalf("token 不一致应返回 403，实际 %d", resp.GetStatus())
	}
}

// TestCsrfAllowsMatchingToken 验证 Cookie 与请求头 token 一致时放行。
func TestCsrfAllowsMatchingToken(t *testing.T) {
	handler := Csrf()
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/save", nil)
	raw.AddCookie(&http.Cookie{Name: DefaultCSRFCookieName, Value: "same-token"})
	raw.Header.Set(DefaultCSRFHeaderName, "same-token")
	req := fwcontext.NewRequest(raw)

	called := false
	resp := handler(req, func(r *fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("done")
	})
	if !called || resp.GetStatus() != http.StatusOK {
		t.Fatalf("匹配的 CSRF token 应放行，called=%v status=%d", called, resp.GetStatus())
	}
}
