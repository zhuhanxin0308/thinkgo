package cookie

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestIssue586RejectsExpireExceedingSignMaxAge 验证单次或配置 Expire 超过 SignMaxAge 时拒绝静默截断签名寿命。
func TestIssue586RejectsExpireExceedingSignMaxAge(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Secret = "0123456789abcdef0123456789abcdef"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	c, err := NewCookieForRequest(cfg, req, rec)
	if err != nil {
		t.Fatal(err)
	}

	err = c.Set("remember", "uid-1", CookieOptions{Expire: 30 * 24 * 3600})
	if !errors.Is(err, ErrInvalidCookieOptions) || !strings.Contains(err.Error(), "sign_max_age") {
		t.Fatalf("30 天 Expire 超过默认 7 天 SignMaxAge 时必须明确返回 ErrInvalidCookieOptions，实际: %v", err)
	}
	if len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("冲突配置下不得向响应写入半截寿命的 Cookie: %#v", rec.Header().Values("Set-Cookie"))
	}

	// 显式调大 SignMaxAge 后，30 天记住登录 Cookie 的 Max-Age 与签名寿命一致，第 20 天仍可正常验签。
	longCfg := cfg
	longCfg.SignMaxAge = 30 * 24 * 3600
	longRec := httptest.NewRecorder()
	longCookie, err := NewCookieForRequest(longCfg, req, longRec)
	if err != nil {
		t.Fatal(err)
	}
	if err := longCookie.Set("remember", "uid-1", CookieOptions{Expire: 30 * 24 * 3600}); err != nil {
		t.Fatalf("调大 SignMaxAge 后写入 30 天 Cookie 失败: %v", err)
	}
	written := longRec.Result().Cookies()
	if len(written) != 1 || written[0].MaxAge != 30*24*3600 {
		t.Fatalf("写入的 30 天 Cookie Max-Age 错误: %#v", written)
	}
	got, err := unsignCookieValue("remember", written[0].Value, longCfg.Secret, longCfg.SignMaxAge, longCfg.SignMaxAge, time.Now().Add(20*24*time.Hour))
	if err != nil || got != "uid-1" {
		t.Fatalf("第 20 天验签 30 天记住我 Cookie 应成功，实际 got=%q err=%v", got, err)
	}
}

// TestIssue586LoopbackForwardedProtoUsesRightMostHop 验证本机回环反代下不会被客户端预置的首个 X-Forwarded-Proto 伪造 Secure。
func TestIssue586LoopbackForwardedProtoUsesRightMostHop(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		headers    []string
		wantSecure bool
	}{
		{
			name:       "客户端伪造 https 后本机反代追加 http（多行头）",
			remoteAddr: "127.0.0.1:54321",
			headers:    []string{"https", "http"},
			wantSecure: false,
		},
		{
			name:       "客户端伪造 https 后本机反代追加 http（逗号合并）",
			remoteAddr: "127.0.0.1:54321",
			headers:    []string{"https, http"},
			wantSecure: false,
		},
		{
			name:       "客户端发送 http 后本机 TLS 反代追加 https",
			remoteAddr: "127.0.0.1:54321",
			headers:    []string{"http", "https"},
			wantSecure: true,
		},
		{
			name:       "带 Zone ID 的 IPv6 回环反代追加 http",
			remoteAddr: "[::1%lo0]:54321",
			headers:    []string{"https", "http"},
			wantSecure: false,
		},
		{
			name:       "带 Zone ID 的 IPv6 回环反代追加 https",
			remoteAddr: "[::1%lo0]:54321",
			headers:    []string{"http", "HTTPS"},
			wantSecure: true,
		},
		{
			name:       "末行为空白值不得提升为 Secure",
			remoteAddr: "127.0.0.1:54321",
			headers:    []string{"https", " "},
			wantSecure: false,
		},
		{
			name:       "非回环来源即使末行 https 也不信任",
			remoteAddr: "198.51.100.12:54321",
			headers:    []string{"http", "https"},
			wantSecure: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			req.RemoteAddr = tc.remoteAddr
			for _, h := range tc.headers {
				req.Header.Add("X-Forwarded-Proto", h)
			}
			rec := httptest.NewRecorder()
			c, err := NewCookieForRequest(DefaultConfig(), req, rec)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Set("sid", "value"); err != nil {
				t.Fatal(err)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Secure != tc.wantSecure {
				t.Fatalf("Secure 属性错误: got=%v want=%v headers=%#v", cookies[0].Secure, tc.wantSecure, tc.headers)
			}
		})
	}
}
