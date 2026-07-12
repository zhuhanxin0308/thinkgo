package middleware

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	fwcontext "thinkgo/framework/context"
)

// newCSRFHandler 创建通过严格配置校验的 CSRF 测试中间件。
func newCSRFHandler(t *testing.T, config CSRFConfig) Handler {
	t.Helper()
	handler, err := CsrfWithConfig(config)
	if err != nil {
		t.Fatalf("创建 CSRF 中间件失败: %v", err)
	}
	return handler
}

// responseCookies 解析框架响应中的所有 Set-Cookie 字段。
func responseCookies(response *fwcontext.Response) []*http.Cookie {
	return (&http.Response{Header: response.Headers()}).Cookies()
}

// issueCSRFTokenCookie 通过安全请求获取真实签名 token Cookie。
func issueCSRFTokenCookie(t *testing.T, handler Handler) *http.Cookie {
	t.Helper()
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://example.com/", nil))
	resp := handler(req, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	})
	cookies := responseCookies(resp)
	if len(cookies) != 1 {
		t.Fatalf("安全方法应下发一个 CSRF Cookie，实际为 %d", len(cookies))
	}
	return cookies[0]
}

// TestParseCSRFConfigStrictlyValidatesPolicy 验证 CSRF 配置拒绝未知字段、危险安全方法和弱密钥。
func TestParseCSRFConfigStrictlyValidatesPolicy(t *testing.T) {
	config, err := ParseCSRFConfig(map[string]interface{}{
		"cookie_name": "csrf", "header_name": "X-CSRF", "field_name": "_token",
		"cookie_path": "/app", "cookie_domain": "example.com", "secure": true,
		"samesite": "Strict", "max_age": float64(600),
		"secret":       strings.Repeat("s", 32),
		"safe_methods": []interface{}{http.MethodGet, http.MethodHead, http.MethodOptions},
	})
	if err != nil {
		t.Fatalf("合法 CSRF 配置解析失败: %v", err)
	}
	if config.CookieName != "csrf" || config.HeaderName != "X-CSRF" || config.MaxAge != 600 || !config.Secure {
		t.Fatalf("CSRF 配置解析结果错误: %#v", config)
	}

	invalid := []map[string]interface{}{
		{"unknown": true},
		{"cookie_name": "bad name"},
		{"header_name": "bad header"},
		{"field_name": "bad field"},
		{"cookie_path": "relative"},
		{"secret": "short"},
		{"samesite": "invalid"},
		{"max_age": 1.5},
		{"max_age": 0},
		{"safe_methods": []interface{}{http.MethodGet, http.MethodPost}},
		{"safe_methods": []interface{}{http.MethodGet, http.MethodGet}},
	}
	for _, raw := range invalid {
		if parsed, parseErr := ParseCSRFConfig(raw); !errors.Is(parseErr, ErrInvalidCSRFConfig) || !reflect.DeepEqual(parsed, CSRFConfig{}) {
			t.Fatalf("非法 CSRF 配置 %#v 应失败: parsed=%#v err=%v", raw, parsed, parseErr)
		}
	}
}

// TestCSRFSafeMethodIssuesSignedSecureToken 验证安全请求下发可验签、非 HttpOnly 且符合传输策略的 Cookie。
func TestCSRFSafeMethodIssuesSignedSecureToken(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("k", 32)
	handler := newCSRFHandler(t, config)
	written := issueCSRFTokenCookie(t, handler)
	if written.Name != DefaultCSRFCookieName || written.HttpOnly || !written.Secure || written.SameSite != http.SameSiteLaxMode {
		t.Fatalf("CSRF Cookie 属性错误: %#v", written)
	}
	if _, err := verifyCSRFToken(config.CookieName, written.Value, config.Secret, config.MaxAge, time.Now()); err != nil {
		t.Fatalf("下发的 CSRF token 无法验签: %v", err)
	}
}

// TestCSRFAllowsOnlyMatchingValidSignedToken 验证合法签名 token 可通过请求头与表单提交。
func TestCSRFAllowsOnlyMatchingValidSignedToken(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("m", 32)
	handler := newCSRFHandler(t, config)
	written := issueCSRFTokenCookie(t, handler)

	requests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "https://example.com/save", nil),
		httptest.NewRequest(http.MethodPost, "https://example.com/save",
			strings.NewReader(url.Values{config.FieldName: {written.Value}}.Encode())),
	}
	requests[0].Header.Set(config.HeaderName, written.Value)
	requests[1].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, raw := range requests {
		raw.AddCookie(written)
		req := fwcontext.MustNewRequest(raw)
		called := false
		resp := handler(req, func(*fwcontext.Request) *fwcontext.Response {
			called = true
			return fwcontext.NewResponse().Content("done")
		})
		if !called || resp.GetStatus() != http.StatusOK {
			t.Fatalf("合法 CSRF token 应放行: called=%t status=%d", called, resp.GetStatus())
		}
	}
}

// TestCSRFRejectsCookieInjectionTamperingAndDuplicates 验证匹配但不可验签的注入值及重复 Cookie 均失败。
func TestCSRFRejectsCookieInjectionTamperingAndDuplicates(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("p", 32)
	handler := newCSRFHandler(t, config)
	valid := issueCSRFTokenCookie(t, handler)
	signatureStart := strings.LastIndex(valid.Value, ".") + 1
	replacement := "x"
	if valid.Value[signatureStart:signatureStart+1] == replacement {
		replacement = "y"
	}
	tampered := valid.Value[:signatureStart] + replacement + valid.Value[signatureStart+1:]

	tests := []struct {
		name   string
		cookie string
		header string
	}{
		{name: "注入相同裸值", cookie: "attacker", header: "attacker"},
		{name: "篡改签名", cookie: tampered, header: tampered},
		{name: "签名值不匹配", cookie: valid.Value, header: valid.Value + "x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodPost, "https://example.com/save", nil)
			raw.AddCookie(&http.Cookie{Name: config.CookieName, Value: test.cookie})
			raw.Header.Set(config.HeaderName, test.header)
			called := false
			resp := handler(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
				called = true
				return fwcontext.NewResponse()
			})
			if called || resp.GetStatus() != http.StatusForbidden {
				t.Fatalf("非法 CSRF token 应返回 403: called=%t status=%d", called, resp.GetStatus())
			}
		})
	}

	duplicate := httptest.NewRequest(http.MethodPost, "https://example.com/save", nil)
	duplicate.Header.Set("Cookie", config.CookieName+"="+valid.Value+"; "+config.CookieName+"="+valid.Value)
	duplicate.Header.Set(config.HeaderName, valid.Value)
	resp := handler(fwcontext.MustNewRequest(duplicate), func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("重复 CSRF Cookie 不应进入下游")
		return nil
	})
	if resp.GetStatus() != http.StatusForbidden {
		t.Fatalf("重复 CSRF Cookie 应返回 403，实际为 %d", resp.GetStatus())
	}
}

// TestCSRFRejectsMissingExpiredAndFutureTokens 验证缺失、过期与未来签名都不会放行。
func TestCSRFRejectsMissingExpiredAndFutureTokens(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("z", 32)
	config.MaxAge = 60
	handler := newCSRFHandler(t, config)
	now := time.Now().Truncate(time.Second)
	nonce, err := generateCSRFToken(strings.NewReader(strings.Repeat("n", csrfTokenBytes)))
	if err != nil {
		t.Fatalf("生成测试 nonce 失败: %v", err)
	}
	expired, _ := signCSRFToken(config.CookieName, nonce, config.Secret, now.Add(-61*time.Second))
	future, _ := signCSRFToken(config.CookieName, nonce, config.Secret, now.Add(maxCSRFClockSkew+time.Second))
	for _, token := range []string{"", expired, future} {
		raw := httptest.NewRequest(http.MethodPost, "https://example.com/save", nil)
		if token != "" {
			raw.AddCookie(&http.Cookie{Name: config.CookieName, Value: token})
			raw.Header.Set(config.HeaderName, token)
		}
		resp := handler(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
			t.Fatal("无效时间 token 不应进入下游")
			return nil
		})
		if resp.GetStatus() != http.StatusForbidden {
			t.Fatalf("无效 token 应返回 403，实际为 %d", resp.GetStatus())
		}
	}
}

// TestCSRFAllowsMultipartToken 验证 multipart 表单字段使用同一签名校验路径。
func TestCSRFAllowsMultipartToken(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("q", 32)
	handler := newCSRFHandler(t, config)
	written := issueCSRFTokenCookie(t, handler)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField(config.FieldName, written.Value); err != nil {
		t.Fatalf("写入 multipart 字段失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	raw := httptest.NewRequest(http.MethodPost, "https://example.com/upload", body)
	raw.Header.Set("Content-Type", writer.FormDataContentType())
	raw.AddCookie(written)
	called := false
	resp := handler(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("done")
	})
	if !called || resp.GetStatus() != http.StatusOK {
		t.Fatalf("合法 multipart token 应放行: called=%t status=%d", called, resp.GetStatus())
	}
}

// TestCSRFConfigIsImmutableAndNoneForcesSecure 验证调用方后续修改切片不会改变策略，且 None 强制 Secure。
func TestCSRFConfigIsImmutableAndNoneForcesSecure(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("r", 32)
	config.SameSite = "None"
	config.SafeMethods = []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	handler := newCSRFHandler(t, config)
	config.SafeMethods[0] = http.MethodPost
	written := issueCSRFTokenCookie(t, handler)
	if !written.Secure || written.SameSite != http.SameSiteNoneMode {
		t.Fatalf("SameSite=None 必须强制 Secure: %#v", written)
	}
	post := httptest.NewRequest(http.MethodPost, "https://example.com/save", nil)
	resp := handler(fwcontext.MustNewRequest(post), func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("修改外部 SafeMethods 不得使 POST 变成安全方法")
		return nil
	})
	if resp.GetStatus() != http.StatusForbidden {
		t.Fatalf("无 token POST 应返回 403，实际为 %d", resp.GetStatus())
	}
}
