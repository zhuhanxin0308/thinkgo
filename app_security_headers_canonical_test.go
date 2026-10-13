package framework

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

func TestAppSecurityHeadersCanonicalConfiguration(t *testing.T) {
	values := map[string]interface{}{
		"enable":                       true,
		"referrer_policy":              "No-Referrer",
		"cross_origin_opener_policy":   "Same-Origin-Allow-Popups",
		"cross_origin_resource_policy": "Same-Site",
		"frame_options":                "sameorigin",
		"content_security_policy":      "script-src 'nonce-AbCdEf0123' https://CDN.example/CaseSensitive",
		"permissions_policy":           `camera=("https://Example.test/CaseSensitive")`,
	}
	handler, enabled, err := createAppSecurityHeaders(values)
	if err != nil || !enabled || handler == nil {
		t.Fatalf("create enabled policy: enabled=%v, handler nil=%v, error=%v", enabled, handler == nil, err)
	}
	writer := httptest.NewRecorder()
	request, err := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "https://example.com/", nil), fwcontext.WithResponseWriter(writer))
	if err != nil {
		t.Fatal(err)
	}
	response := handler(request, func(*fwcontext.Request) *fwcontext.Response {
		writer.WriteHeader(http.StatusNoContent)
		return fwcontext.NewResponse()
	})
	result := writer.Result()
	defer result.Body.Close()
	for name, want := range map[string]string{
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin-allow-popups",
		"Cross-Origin-Resource-Policy": "same-site",
		"X-Frame-Options":              "SAMEORIGIN",
		"Content-Security-Policy":      values["content_security_policy"].(string),
		"Permissions-Policy":           values["permissions_policy"].(string),
	} {
		if got := response.Headers().Get(name); got != want {
			t.Errorf("framework %s=%q, want %q", name, got, want)
		}
		if got := result.Header.Get(name); got != want {
			t.Errorf("committed %s=%q, want %q", name, got, want)
		}
	}
	if values["cross_origin_resource_policy"] != "Same-Site" {
		t.Error("application configuration was mutated")
	}
}

func TestAppSecurityHeadersCanonicalizationDoesNotEnableOrRelaxPolicy(t *testing.T) {
	for _, values := range []map[string]interface{}{nil, {}, {"enable": false}, {"cross_origin_resource_policy": "Same-Site"}} {
		handler, enabled, err := createAppSecurityHeaders(values)
		if handler != nil || enabled || err != nil {
			t.Errorf("disabled policy changed: enabled=%v, handler nil=%v, error=%v", enabled, handler == nil, err)
		}
	}
	for _, value := range []string{" Same-Site", "Same-Site\r\nX-Evil: yes", "invalid"} {
		handler, enabled, err := createAppSecurityHeaders(map[string]interface{}{"enable": true, "cross_origin_resource_policy": value})
		if handler != nil || !enabled || !errors.Is(err, middleware.ErrInvalidSecurityHeaders) {
			t.Errorf("invalid policy %q changed: enabled=%v, handler nil=%v, error=%v", value, enabled, handler == nil, err)
		}
	}
}
