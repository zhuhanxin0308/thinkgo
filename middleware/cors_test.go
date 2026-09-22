package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

func newCorsRequest(t *testing.T, method, origin string) *fwcontext.Request {
	t.Helper()
	raw := httptest.NewRequest(method, "https://example.com/resource", nil)
	if origin != "" {
		raw.Header.Set("Origin", origin)
	}
	return fwcontext.MustNewRequest(raw)
}

// newCorsRequestWithWriter 创建绑定原生响应写入器的请求，用于验证已提交响应的头部边界。
func newCorsRequestWithWriter(t *testing.T, method, origin string, writer http.ResponseWriter) *fwcontext.Request {
	t.Helper()
	raw := httptest.NewRequest(method, "https://example.com/resource", nil)
	if origin != "" {
		raw.Header.Set("Origin", origin)
	}
	request, err := fwcontext.NewRequest(raw, fwcontext.WithResponseWriter(writer))
	if err != nil {
		t.Fatalf("创建带响应写入器的 CORS 请求失败: %v", err)
	}
	return request
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
	if got := response.Headers().Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("普通响应不应设置允许方法: %q", got)
	}
	if got := response.Headers().Get("Access-Control-Max-Age"); got != "" {
		t.Fatalf("普通响应不应设置预检缓存时间: %q", got)
	}

	preflightRequest := newCorsRequest(t, http.MethodOptions, "https://client.example")
	preflightRequest.Raw().Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflight := handler(preflightRequest, func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("预检请求不应进入下游")
		return nil
	})
	if preflight.GetStatus() != http.StatusNoContent || preflight.Headers().Get("Access-Control-Allow-Methods") == "" {
		t.Fatalf("默认预检响应错误: %#v", preflight)
	}
	if got := preflight.Headers().Get("Access-Control-Max-Age"); got != "86400" {
		t.Fatalf("通配符 CORS 预检缓存时间错误: %q", got)
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
	if headers.Get("Access-Control-Max-Age") != "" || headers.Get("Access-Control-Allow-Headers") != "" || headers.Get("Access-Control-Allow-Methods") != "" {
		t.Fatalf("普通响应不应携带仅供预检使用的响应头: %#v", headers)
	}
}

// TestCorsPreservesExistingVaryHeaders 验证 CORS 不会覆盖缓存协商所需的既有 Vary。
func TestCorsPreservesExistingVaryHeaders(t *testing.T) {
	handler := CorsWithConfig(CorsConfig{AllowOrigins: []string{"https://client.example"}, AllowMethods: []string{http.MethodGet}})
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
	preflightRequest := newCorsRequest(t, http.MethodOptions, "https://allowed.example")
	preflightRequest.Raw().Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflightRequest.Raw().Header.Set("Access-Control-Request-Headers", "Content-Type")
	preflight := handler(preflightRequest, func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Code(http.StatusOK)
	})
	if called || preflight.GetStatus() != http.StatusNoContent {
		t.Fatalf("合法预检请求应直接返回 204: called=%t status=%d", called, preflight.GetStatus())
	}
	if preflight.Headers().Get("Access-Control-Max-Age") != "" {
		t.Fatal("MaxAge 为零时不应设置缓存头")
	}
	if preflight.Headers().Get("Access-Control-Expose-Headers") != "" {
		t.Fatal("预检响应不应携带仅供普通响应使用的暴露头")
	}
	if got := preflight.Headers().Get("Vary"); got != "Origin, Access-Control-Request-Method, Access-Control-Request-Headers" {
		t.Fatalf("预检响应 Vary 不完整: %q", got)
	}

	called = false
	rejectedRequest := newCorsRequest(t, http.MethodOptions, "https://denied.example")
	rejectedRequest.Raw().Header.Set("Access-Control-Request-Method", http.MethodPost)
	rejected := handler(rejectedRequest, func(*fwcontext.Request) *fwcontext.Response {
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

// TestCorsLetsActualRequestReachRouter 验证 AllowMethods 只约束预检，实际请求仍由业务路由决定是否支持。
func TestCorsLetsActualRequestReachRouter(t *testing.T) {
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
	if !called || response == nil || response.GetStatus() != http.StatusOK {
		t.Fatalf("实际请求应进入业务路由: called=%t response=%#v", called, response)
	}
	if response.Headers().Get("Access-Control-Allow-Origin") != "https://allowed.example" {
		t.Fatalf("实际请求缺少允许来源响应头: %#v", response.Headers())
	}
}

// TestCorsDistinguishesOrdinaryOptionsFromPreflight 验证普通 OPTIONS 不会被误判为 CORS 预检。
func TestCorsDistinguishesOrdinaryOptionsFromPreflight(t *testing.T) {
	handler, err := NewCors(CorsConfig{
		AllowOrigins: []string{"https://allowed.example"},
		AllowMethods: []string{http.MethodPost},
	})
	if err != nil {
		t.Fatalf("创建 CORS 中间件失败: %v", err)
	}

	t.Run("没有 Origin", func(t *testing.T) {
		called := false
		response := handler(newCorsRequest(t, http.MethodOptions, ""), func(*fwcontext.Request) *fwcontext.Response {
			called = true
			return fwcontext.NewResponse().Header("Allow", "GET, OPTIONS").NoContent()
		})
		if !called || response.GetStatus() != http.StatusNoContent || response.Headers().Get("Allow") != "GET, OPTIONS" {
			t.Fatalf("普通 OPTIONS 应完整进入下游: called=%t response=%#v", called, response)
		}
		if response.Headers().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("无 Origin 请求不应添加允许来源: %#v", response.Headers())
		}
	})

	t.Run("没有请求方法头", func(t *testing.T) {
		called := false
		response := handler(newCorsRequest(t, http.MethodOptions, "https://allowed.example"), func(*fwcontext.Request) *fwcontext.Response {
			called = true
			return fwcontext.NewResponse().Header("Allow", "GET, OPTIONS").NoContent()
		})
		if !called || response.GetStatus() != http.StatusNoContent || response.Headers().Get("Allow") != "GET, OPTIONS" {
			t.Fatalf("缺少预检方法头的 OPTIONS 应进入下游: called=%t response=%#v", called, response)
		}
		if response.Headers().Get("Access-Control-Allow-Origin") != "https://allowed.example" {
			t.Fatalf("普通跨域 OPTIONS 应携带实际响应头: %#v", response.Headers())
		}
		if response.Headers().Get("Access-Control-Allow-Methods") != "" || response.Headers().Get("Access-Control-Max-Age") != "" {
			t.Fatalf("普通 OPTIONS 不应携带预检响应头: %#v", response.Headers())
		}
	})
}

// TestCorsWritesHeadersBeforeCommittedHandler 验证标准库 Handler 提交响应前已经获得 CORS 头。
func TestCorsWritesHeadersBeforeCommittedHandler(t *testing.T) {
	handler, err := NewCors(CorsConfig{
		AllowOrigins:     []string{"https://allowed.example"},
		AllowMethods:     []string{http.MethodGet},
		ExposeHeaders:    []string{"X-Trace-ID"},
		AllowCredentials: true,
	})
	if err != nil {
		t.Fatalf("创建 CORS 中间件失败: %v", err)
	}
	writer := httptest.NewRecorder()
	request := newCorsRequestWithWriter(t, http.MethodGet, "https://allowed.example", writer)
	response := handler(request, func(*fwcontext.Request) *fwcontext.Response {
		if writer.Header().Get("Access-Control-Allow-Origin") != "https://allowed.example" {
			t.Fatal("下游提交前必须已经写入允许来源")
		}
		writer.WriteHeader(http.StatusCreated)
		return fwcontext.NewCommittedResponse(http.StatusCreated)
	})
	if response == nil || response.GetStatus() != http.StatusCreated {
		t.Fatalf("已提交响应状态错误: %#v", response)
	}
	if writer.Header().Get("Access-Control-Allow-Credentials") != "true" || writer.Header().Get("Access-Control-Expose-Headers") != "X-Trace-ID" {
		t.Fatalf("原生响应写入器缺少 CORS 头: %#v", writer.Header())
	}
}

// TestNewCorsRejectsAmbiguousSecurityConfiguration 验证启动期构造会拒绝空方法和凭证通配符。
func TestNewCorsRejectsAmbiguousSecurityConfiguration(t *testing.T) {
	tests := []CorsConfig{
		{AllowOrigins: []string{"https://allowed.example"}},
		{AllowOrigins: []string{"*"}, AllowMethods: []string{http.MethodGet}, AllowCredentials: true},
		{AllowOrigins: []string{"https://allowed.example"}, AllowMethods: []string{"*"}, AllowCredentials: true},
		{AllowOrigins: []string{"https://allowed.example"}, AllowMethods: []string{http.MethodGet}, AllowHeaders: []string{"*"}, AllowCredentials: true},
		{AllowOrigins: []string{"https://allowed.example"}, AllowMethods: []string{http.MethodGet}, ExposeHeaders: []string{"*"}, AllowCredentials: true},
	}
	for _, config := range tests {
		if _, err := NewCors(config); err == nil {
			t.Fatalf("不明确或不安全的 CORS 配置必须被拒绝: %#v", config)
		}
	}
}

// TestCorsWildcardHeadersDoNotCoverAuthorization 验证 Authorization 必须显式列入允许头。
func TestCorsWildcardHeadersDoNotCoverAuthorization(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []string
		status  int
	}{
		{name: "仅通配符", headers: []string{"*"}, status: http.StatusForbidden},
		{name: "显式授权头", headers: []string{"*", "Authorization"}, status: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewCors(CorsConfig{
				AllowOrigins: []string{"*"},
				AllowMethods: []string{http.MethodPost},
				AllowHeaders: test.headers,
			})
			if err != nil {
				t.Fatalf("创建 CORS 中间件失败: %v", err)
			}
			request := newCorsRequest(t, http.MethodOptions, "https://client.example")
			request.Raw().Header.Set("Access-Control-Request-Method", http.MethodPost)
			request.Raw().Header.Set("Access-Control-Request-Headers", "Authorization")
			response := handler(request, func(*fwcontext.Request) *fwcontext.Response {
				t.Fatal("预检请求不应进入下游")
				return nil
			})
			if response.GetStatus() != test.status {
				t.Fatalf("Authorization 通配符判定错误: status=%d headers=%#v", response.GetStatus(), response.Headers())
			}
		})
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
		AllowMethods:     []string{http.MethodGet},
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
	handler := CorsWithConfig(CorsConfig{AllowOrigins: []string{"https://client.example"}, AllowMethods: []string{http.MethodGet}})
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
