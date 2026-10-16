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
