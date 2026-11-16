package context_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestResponseCookieUnknownOptionsDoNotEmitCookie(t *testing.T) {
	for name, options := range map[string]interface{}{
		"secure-typo":        map[string]interface{}{"secrue": true, "httponly": true},
		"unknown-string-map": map[string]string{"path": "/", "same_site": "Strict"},
		"empty-key":          map[string]interface{}{"": true},
		"whitespace-key":     map[string]interface{}{" \t ": true},
		"unsupported-policy": map[string]interface{}{"partitioned": true},
	} {
		t.Run(name, func(t *testing.T) {
			response := fwcontext.NewResponse().Cookie("existing", "preserved")
			before := response.Headers().Values("Set-Cookie")
			if got := response.Cookie("session", "secret", options); got != response {
				t.Fatal("Cookie must preserve fluent return")
			}
			if !errors.Is(response.Error(), fwcontext.ErrInvalidResponseHeader) {
				t.Fatalf("unknown policy ignored: %v", response.Error())
			}
			if strings.Contains(response.Error().Error(), "secret") {
				t.Fatalf("Cookie value leaked in error: %v", response.Error())
			}
			if !reflect.DeepEqual(before, response.Headers().Values("Set-Cookie")) {
				t.Fatalf("invalid Cookie changed headers: %#v", response.Headers())
			}
			if cookies := (&http.Response{Header: response.Headers()}).Cookies(); len(cookies) != 1 || cookies[0].Name != "existing" {
				t.Fatalf("invalid Cookie was emitted: %#v", cookies)
			}
			recorder := httptest.NewRecorder()
			if err := response.Send(recorder); !errors.Is(err, fwcontext.ErrInvalidResponseHeader) {
				t.Fatalf("Send must report the policy error: %v", err)
			}
			if cookies := recorder.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != "existing" {
				t.Fatalf("invalid Cookie reached wire: %#v", cookies)
			}
		})
	}
}

func TestResponseCookieKnownOptionsRetainPolicy(t *testing.T) {
	response := fwcontext.NewResponse().Cookie("session", "safe", map[string]interface{}{
		" Secure ": true, "HTTPONLY": true, " SameSite ": "Strict", "path": "/private", "domain": "example.com", "expire": 3600,
	})
	if err := response.Error(); err != nil {
		t.Fatal(err)
	}
	cookies := (&http.Response{Header: response.Headers()}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %#v", cookies)
	}
	c := cookies[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/private" || c.Domain != "example.com" || c.MaxAge != 0 || c.Expires.IsZero() {
		t.Fatalf("valid policy changed: %#v", c)
	}
	stringMap := fwcontext.NewResponse().Cookie("lang", "zh", map[string]string{" PATH ": "/"})
	if stringMap.Error() != nil || len((&http.Response{Header: stringMap.Headers()}).Cookies()) != 1 {
		t.Fatalf("known string-map option rejected: %v", stringMap.Error())
	}
}

func TestResponseCookieDefaultPolicyAndExplicitOverrides(t *testing.T) {
	for _, test := range []struct {
		name     string
		options  []any
		httpOnly bool
		sameSite http.SameSite
	}{
		{"默认", nil, true, http.SameSiteLaxMode},
		{"仅指定过期", []any{300}, true, http.SameSiteLaxMode},
		{"映射覆盖", []any{map[string]any{"httponly": false, "samesite": "Strict"}}, false, http.SameSiteStrictMode},
		{"空SameSite", []any{map[string]string{"samesite": ""}}, true, 0},
		{"位置参数覆盖", []any{300, "/", "", false, false}, false, http.SameSiteLaxMode},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fwcontext.NewResponse().Cookie("preference", "zh", test.options...)
			recorder := httptest.NewRecorder()
			if err := response.Send(recorder); err != nil {
				t.Fatal(err)
			}
			cookies := recorder.Result().Cookies()
			if len(cookies) != 1 || cookies[0].HttpOnly != test.httpOnly || cookies[0].SameSite != test.sameSite {
				t.Fatalf("响应输出的 Cookie 属性错误: %s", recorder.Header().Get("Set-Cookie"))
			}
		})
	}
}

func TestResponseCookieRejectsDuplicateNormalizedOptions(t *testing.T) {
	for name, options := range map[string]interface{}{
		"Secure 大小写":  map[string]interface{}{"Secure": true, "secure": false},
		"HttpOnly 空格": map[string]interface{}{"HTTPONLY": true, " httponly ": false},
		"SameSite 冲突": map[string]string{"SameSite": "Strict", " samesite ": "Lax"},
		"相同规范值":       map[string]string{"Path": "/api", " path ": "/api"},
	} {
		t.Run(name, func(t *testing.T) {
			response := fwcontext.NewResponse().Cookie("existing", "safe")
			response.Cookie("session", "invalid", options)
			if !errors.Is(response.Error(), fwcontext.ErrInvalidResponseHeader) || !strings.Contains(response.Error().Error(), "重复") {
				t.Fatalf("重复 Cookie 选项应明确报错: %v", response.Error())
			}
			recorder := httptest.NewRecorder()
			if err := response.Send(recorder); !errors.Is(err, fwcontext.ErrInvalidResponseHeader) {
				t.Fatalf("实际发送应返回头部错误: %v", err)
			}
			cookies := recorder.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != "existing" {
				t.Fatalf("重复选项生成了歧义 Cookie: %#v", cookies)
			}
		})
	}
}
