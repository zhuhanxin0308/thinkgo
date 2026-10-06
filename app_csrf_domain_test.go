package framework

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

func TestIssue43AppCSRFPurposeIsolation(t *testing.T) {
	const name = middleware.DefaultCSRFCookieName
	secret := strings.Repeat("k", 32)
	config := cookie.DefaultConfig()
	config.Secret = secret
	factory, err := cookie.NewCookieWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	header, err := factory.BuildHeader(name, strings.Repeat("n", 32))
	if err != nil {
		t.Fatal(err)
	}
	signedCookies := (&http.Response{Header: http.Header{"Set-Cookie": {header}}}).Cookies()
	if len(signedCookies) != 1 {
		t.Fatal("expected one generic signed Cookie")
	}
	for _, explicit := range []bool{false, true} {
		mode := "cookie_secret_default"
		rawConfig := map[string]interface{}{}
		if explicit {
			mode = "explicit_same_secret"
			rawConfig["secret"] = secret
		}
		t.Run(mode, func(t *testing.T) {
			handler, err := createAppCSRF(rawConfig, factory)
			if err != nil {
				t.Fatal(err)
			}
			otherInstance, err := createAppCSRF(rawConfig, factory)
			if err != nil {
				t.Fatal(err)
			}
			get := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://example.com/", nil))
			response := handler(get, func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("ok") })
			current := (&http.Response{Header: response.Headers()}).Cookies()
			if len(current) != 1 {
				t.Fatal("expected one CSRF Cookie")
			}
			for _, test := range []struct {
				name, token string
				status      int
			}{
				{"foreign", signedCookies[0].Value, http.StatusForbidden},
				{"relabeled_foreign", "csrf-v1." + strings.TrimPrefix(signedCookies[0].Value, "v1."), http.StatusForbidden},
				{"current", current[0].Value, http.StatusOK},
			} {
				t.Run(test.name, func(t *testing.T) {
					request := httptest.NewRequest(http.MethodPost, "https://example.com/save", nil)
					request.Header.Set("Origin", "https://example.com")
					request.Header.Set(middleware.DefaultCSRFHeaderName, test.token)
					request.AddCookie(&http.Cookie{Name: name, Value: test.token})
					called := false
					result := otherInstance(fwcontext.MustNewRequest(request), func(*fwcontext.Request) *fwcontext.Response {
						called = true
						return fwcontext.NewResponse().Content("ok")
					})
					if result == nil || result.GetStatus() != test.status || called != (test.status == http.StatusOK) {
						t.Fatalf("application CSRF boundary failed: called=%t response=%v want=%d", called, result, test.status)
					}
				})
			}
		})
	}
}
