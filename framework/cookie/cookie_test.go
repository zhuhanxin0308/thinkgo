package cookie

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestParseConfigKeepsSecureDefaultsWhenSameSiteBlank 验证空的 SameSite 配置不会把安全默认值清空。
func TestParseConfigKeepsSecureDefaultsWhenSameSiteBlank(t *testing.T) {
	cfg := ParseConfig(map[string]interface{}{
		"samesite": "",
	})

	if cfg.SameSite != "Lax" {
		t.Fatalf("空 SameSite 应回退为 Lax，实际为 %q", cfg.SameSite)
	}
}

// TestSetMarksSecureBehindLoopbackHTTPSProxy 验证本机 HTTPS 反代后的 Cookie 会自动加 Secure。
func TestSetMarksSecureBehindLoopbackHTTPSProxy(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	raw.RemoteAddr = "127.0.0.1:54321"
	raw.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()

	manager := NewCookieForRequest(ParseConfig(map[string]interface{}{}), raw, recorder)
	manager.Set("sid", "abc")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("应写入一个 Cookie，实际为 %d", len(cookies))
	}
	if !cookies[0].Secure {
		t.Fatal("本机 HTTPS 反代后的 Cookie 应自动开启 Secure")
	}
}

// TestSetIgnoresSpoofedHTTPSProxyHeaderFromRemote 验证远程客户端伪造代理头不会影响 Cookie 策略。
func TestSetIgnoresSpoofedHTTPSProxyHeaderFromRemote(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	raw.RemoteAddr = "198.51.100.10:54321"
	raw.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()

	manager := NewCookieForRequest(ParseConfig(map[string]interface{}{}), raw, recorder)
	manager.Set("sid", "abc")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("应写入一个 Cookie，实际为 %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Fatal("远程伪造 X-Forwarded-Proto 不应让 Cookie 自动开启 Secure")
	}
}

// TestCookieBeforeInitIsSafe 验证旧 API 未绑定请求/响应时不会因空指针 panic。
func TestCookieBeforeInitIsSafe(t *testing.T) {
	manager := NewCookie(map[string]interface{}{})

	if value := manager.Get("sid"); value != "" {
		t.Fatalf("未绑定请求时 Get 应返回空字符串，实际为 %q", value)
	}
	if manager.Has("sid") {
		t.Fatal("未绑定请求时 Has 应返回 false")
	}

	manager.Set("sid", "abc")
	manager.Delete("sid")
}

// TestHasRejectsTamperedSignedCookie 验证开启签名后 Has 与 Get 一样必须拒绝被篡改的 Cookie。
func TestHasRejectsTamperedSignedCookie(t *testing.T) {
	cfg := ParseConfig(map[string]interface{}{
		"prefix": "think_",
		"secret": "cookie-secret",
	})
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	recorder := httptest.NewRecorder()
	writer := NewCookieForRequest(cfg, req, recorder)
	writer.Set("sid", "abc")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("应写入一个签名 Cookie，实际为 %d", len(cookies))
	}
	cookies[0].Value = cookies[0].Value + "tampered"

	readReq := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	readReq.AddCookie(cookies[0])
	reader := NewCookieForRequest(cfg, readReq, httptest.NewRecorder())

	if value := reader.Get("sid"); value != "" {
		t.Fatalf("篡改签名 Cookie 的 Get 应返回空字符串，实际为 %q", value)
	}
	if reader.Has("sid") {
		t.Fatal("篡改签名 Cookie 的 Has 应返回 false")
	}
}
