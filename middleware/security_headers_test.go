package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestSecurityHeadersApplyBeforeAndAfterHandler 验证安全头在下游前写入原生 Writer，并覆盖框架响应中的弱值。
func TestSecurityHeadersApplyBeforeAndAfterHandler(t *testing.T) {
	handler, err := NewSecurityHeaders(SecurityHeadersConfig{
		ContentSecurityPolicy: "default-src 'self'",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		PermissionsPolicy:     "camera=(), microphone=()",
		HSTSMaxAgeSeconds:     31536000,
		HSTSIncludeSubDomains: true,
	})
	if err != nil {
		t.Fatalf("创建安全响应头中间件失败: %v", err)
	}
	raw := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	raw.TLS = nil
	raw.Header.Set("X-Forwarded-Proto", "https")
	writer := httptest.NewRecorder()
	request, err := fwcontext.NewRequest(raw, fwcontext.WithResponseWriter(writer), fwcontext.WithTrustedProxies([]string{"10.0.0.0/8"}))
	if err != nil {
		t.Fatalf("创建安全头测试请求失败: %v", err)
	}
	response := handler(request, func(*fwcontext.Request) *fwcontext.Response {
		if writer.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("安全响应头必须在下游处理器执行前写入原生 Writer")
		}
		return fwcontext.NewResponse().Header("X-Frame-Options", "ALLOWALL").Content("ok")
	})
	if response.Headers().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("安全响应头必须覆盖下游弱值: %#v", response.Headers())
	}
	if response.Headers().Get("Content-Security-Policy") != "default-src 'self'" {
		t.Fatalf("CSP 未写入框架响应: %#v", response.Headers())
	}
	if response.Headers().Get("Strict-Transport-Security") != "" {
		t.Fatal("不受信代理声明的 HTTPS 不应启用 HSTS")
	}
}

// TestSecurityHeadersHSTSOnlyForVerifiedHTTPS 验证 HSTS 只在真实 TLS 或可信代理 HTTPS 上发送。
func TestSecurityHeadersHSTSOnlyForVerifiedHTTPS(t *testing.T) {
	handler, err := NewSecurityHeaders(SecurityHeadersConfig{HSTSMaxAgeSeconds: 63072000, HSTSIncludeSubDomains: true, HSTSPreload: true})
	if err != nil {
		t.Fatalf("创建 HSTS 中间件失败: %v", err)
	}
	raw := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	request := fwcontext.MustNewRequest(raw)
	response := handler(request, func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse() })
	if response.Headers().Get("Strict-Transport-Security") != "max-age=63072000; includeSubDomains; preload" {
		t.Fatalf("真实 HTTPS 的 HSTS 错误: %#v", response.Headers())
	}
}

// TestSecurityHeadersRejectInvalidConfiguration 验证换行注入、非法策略和不安全 HSTS preload 组合被拒绝。
func TestSecurityHeadersRejectInvalidConfiguration(t *testing.T) {
	for _, config := range []SecurityHeadersConfig{
		{ContentSecurityPolicy: "default-src 'self'\r\nX-Evil: true"},
		{ReferrerPolicy: "unsafe-custom-policy"},
		{CrossOriginOpenerPolicy: "invalid"},
		{HSTSMaxAgeSeconds: 100, HSTSPreload: true},
	} {
		if _, err := NewSecurityHeaders(config); err == nil {
			t.Fatalf("非法安全响应头配置必须被拒绝: %#v", config)
		}
	}
}
