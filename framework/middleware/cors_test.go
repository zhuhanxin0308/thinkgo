package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "thinkgo/framework/context"
)

func newCorsRequest(t *testing.T, method, origin string) *fwcontext.Request {
	t.Helper()
	raw := httptest.NewRequest(method, "https://example.com/resource", nil)
	if origin != "" {
		raw.Header.Set("Origin", origin)
	}
	return fwcontext.MustNewRequest(raw)
}

func TestDefaultCorsConfigAndWildcardResponse(t *testing.T) {
	config := DefaultCorsConfig()
	if len(config.AllowOrigins) != 1 || config.AllowOrigins[0] != "*" {
		t.Fatalf("默认 CORS 来源配置错误: %#v", config.AllowOrigins)
	}
	if !containsString(config.AllowMethods, http.MethodOptions) || config.MaxAge != 86400 {
		t.Fatalf("默认 CORS 方法或缓存时间错误: %#v", config)
	}

	handler := Cors()
	called := false
	response := handler(newCorsRequest(t, http.MethodGet, "https://client.example"), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("ok")
	})
	if !called || response == nil || response.GetStatus() != http.StatusOK {
		t.Fatalf("通配符 CORS 正常请求未进入下游: called=%t response=%#v", called, response)
	}
	if got := response.Headers().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("通配符 CORS 来源错误: %q", got)
	}
	if got := response.Headers().Get("Vary"); got != "" {
		t.Fatalf("通配符 CORS 不应设置 Vary: %q", got)
	}
	if got := response.Headers().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("通配符 CORS 应设置允许方法")
	}
	if got := response.Headers().Get("Access-Control-Max-Age"); got != "86400" {
		t.Fatalf("通配符 CORS 缓存时间错误: %q", got)
	}
}

func TestCorsNamedOriginCredentialsAndExposeHeaders(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{
		AllowOrigins:     []string{"https://client.example"},
		AllowMethods:     []string{http.MethodGet},
		AllowHeaders:     []string{"X-Request-ID"},
		ExposeHeaders:    []string{"X-Trace-ID"},
		AllowCredentials: true,
		MaxAge:           120,
	})
	response := handler(newCorsRequest(t, http.MethodGet, "https://client.example"), func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	})
	headers := response.Headers()
	if headers.Get("Access-Control-Allow-Origin") != "https://client.example" || headers.Get("Vary") != "Origin" {
		t.Fatalf("命名来源 CORS 响应头错误: %#v", headers)
	}
	if headers.Get("Access-Control-Allow-Credentials") != "true" || headers.Get("Access-Control-Expose-Headers") != "X-Trace-ID" {
		t.Fatalf("凭证或暴露头配置未生效: %#v", headers)
	}
	if headers.Get("Access-Control-Max-Age") != "120" || headers.Get("Access-Control-Allow-Headers") != "X-Request-ID" {
		t.Fatalf("命名来源 CORS 细节头错误: %#v", headers)
	}
}

// TestCorsPreservesExistingVaryHeaders 验证 CORS 不会覆盖缓存协商所需的既有 Vary。
func TestCorsPreservesExistingVaryHeaders(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{AllowOrigins: []string{"https://client.example"}})
	response := handler(newCorsRequest(t, http.MethodGet, "https://client.example"), func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Header("Vary", "Accept-Encoding")
	})
	if got := response.Headers().Get("Vary"); got != "Accept-Encoding, Origin" {
		t.Fatalf("CORS 应合并 Vary，实际为 %q", got)
	}
}

func TestCorsPreflightAndRejectedOrigin(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{
		AllowOrigins: []string{"https://allowed.example"},
		AllowMethods: []string{http.MethodPost},
		AllowHeaders: []string{"Content-Type"},
		MaxAge:       0,
	})
	called := false
	preflight := handler(newCorsRequest(t, http.MethodOptions, "https://allowed.example"), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Code(http.StatusOK)
	})
	if called || preflight.GetStatus() != http.StatusNoContent {
		t.Fatalf("合法预检请求应直接返回 204: called=%t status=%d", called, preflight.GetStatus())
	}
	if preflight.Headers().Get("Access-Control-Max-Age") != "" {
		t.Fatal("MaxAge 为零时不应设置缓存头")
	}

	called = false
	rejected := handler(newCorsRequest(t, http.MethodOptions, "https://denied.example"), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Code(http.StatusOK)
	})
	if called || rejected.GetStatus() != http.StatusForbidden {
		t.Fatalf("非法预检请求应返回 403: called=%t status=%d", called, rejected.GetStatus())
	}

	called = false
	normal := handler(newCorsRequest(t, http.MethodGet, "https://denied.example"), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("ok")
	})
	if !called || normal.GetStatus() != http.StatusOK || normal.Headers().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("非法来源普通请求应继续下游且不附加 CORS: called=%t headers=%#v", called, normal.Headers())
	}
}

// TestCorsPreflightRejectsUnlistedMethodAndHeader 验证预检请求不能绕过方法或请求头白名单。
func TestCorsPreflightRejectsUnlistedMethodAndHeader(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{
		AllowOrigins: []string{"https://allowed.example"},
		AllowMethods: []string{http.MethodPost},
		AllowHeaders: []string{"Content-Type"},
	})
	for _, test := range []struct {
		name            string
		method          string
		requestedHeader string
	}{
		{name: "method", method: http.MethodGet},
		{name: "header", method: http.MethodPost, requestedHeader: "X-Not-Allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := newCorsRequest(t, http.MethodOptions, "https://allowed.example")
			req.Raw().Header.Set("Access-Control-Request-Method", test.method)
			if test.requestedHeader != "" {
				req.Raw().Header.Set("Access-Control-Request-Headers", test.requestedHeader)
			}
			response := handler(req, func(*fwcontext.Request) *fwcontext.Response {
				t.Fatal("拒绝的预检请求不应进入下游")
				return nil
			})
			if response == nil || response.GetStatus() != http.StatusForbidden {
				t.Fatalf("不允许的预检请求必须返回 403，实际响应: %#v", response)
			}
		})
	}
}

// TestCorsRejectsSimpleRequestWithUnlistedMethod 验证普通跨域请求也必须服从 AllowMethods。
func TestCorsRejectsSimpleRequestWithUnlistedMethod(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{
		AllowOrigins:     []string{"https://allowed.example"},
		AllowMethods:     []string{http.MethodPost},
		AllowCredentials: true,
	})
	called := false
	response := handler(newCorsRequest(t, http.MethodGet, "https://allowed.example"), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Code(http.StatusOK)
	})
	if called || response == nil || response.GetStatus() != http.StatusForbidden {
		t.Fatalf("未列入 AllowMethods 的普通跨域请求必须被拒绝: called=%t response=%#v", called, response)
	}
}

// TestCorsDoesNotAddHeadersToNonCorsRequest 验证没有 Origin 的普通请求不应被当作跨域请求。
func TestCorsDoesNotAddHeadersToNonCorsRequest(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{AllowOrigins: []string{"*"}, AllowMethods: []string{http.MethodGet}})
	response := handler(newCorsRequest(t, http.MethodGet, ""), func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Code(http.StatusOK)
	})
	if response == nil || response.GetStatus() != http.StatusOK || response.Headers().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("非跨域普通请求不应附加 CORS 头: %#v", response)
	}
}

// TestCorsInvalidConfigurationFailsClosed 验证无效安全配置不会静默退化为宽松策略。
func TestCorsInvalidConfigurationFailsClosed(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{
		AllowOrigins:     []string{"*"},
		AllowCredentials: true,
	})
	response := handler(newCorsRequest(t, http.MethodGet, "https://client.example"), func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("无效 CORS 配置不应进入下游")
		return nil
	})
	if response == nil || response.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("无效 CORS 配置必须失败关闭，实际响应: %#v", response)
	}
}

func TestCorsHandlesNilDownstreamResponse(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{AllowOrigins: []string{"https://client.example"}})
	response := handler(newCorsRequest(t, http.MethodGet, "https://client.example"), func(*fwcontext.Request) *fwcontext.Response {
		return nil
	})
	if response == nil || response.GetStatus() != http.StatusNoContent {
		t.Fatalf("下游返回 nil 时应生成 204 响应: %#v", response)
	}
	if response.Headers().Get("Access-Control-Allow-Origin") != "https://client.example" {
		t.Fatal("nil 下游响应仍应附加 CORS 头")
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
