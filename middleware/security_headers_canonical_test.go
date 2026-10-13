package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestSecurityHeadersCanonicalTokens checks every accepted enum at both output
// boundaries, including native handlers that commit headers before returning.
func TestSecurityHeadersCanonicalTokens(t *testing.T) {
	fields := []struct {
		header string
		values []string
		set    func(*SecurityHeadersConfig, string)
	}{
		{"Referrer-Policy", []string{"no-referrer", "no-referrer-when-downgrade", "origin", "origin-when-cross-origin", "same-origin", "strict-origin", "strict-origin-when-cross-origin"}, func(c *SecurityHeadersConfig, s string) { c.ReferrerPolicy = s }},
		{"Cross-Origin-Opener-Policy", []string{"unsafe-none", "same-origin-allow-popups", "same-origin"}, func(c *SecurityHeadersConfig, s string) { c.CrossOriginOpenerPolicy = s }},
		{"Cross-Origin-Resource-Policy", []string{"same-site", "same-origin", "cross-origin"}, func(c *SecurityHeadersConfig, s string) { c.CrossOriginResourcePolicy = s }},
		{"X-Frame-Options", []string{"DENY", "SAMEORIGIN"}, func(c *SecurityHeadersConfig, s string) { c.FrameOptions = s }},
	}
	for _, field := range fields {
		for _, want := range field.values {
			for _, input := range []string{strings.ToLower(want), strings.ToUpper(want), strings.ToUpper(want[:1]) + strings.ToLower(want[1:])} {
				for _, mode := range []string{"response", "nil", "early", "stream", "error"} {
					t.Run(field.header+"/"+input+"/"+mode, func(t *testing.T) {
						config := SecurityHeadersConfig{}
						field.set(&config, input)
						handler, err := NewSecurityHeaders(config)
						if err != nil || handler == nil {
							t.Fatalf("accepted enum %q: handler nil=%v, error=%v", input, handler == nil, err)
						}
						writer := httptest.NewRecorder()
						request, err := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "https://example.com/", nil), fwcontext.WithResponseWriter(writer))
						if err != nil {
							t.Fatal(err)
						}
						response := handler(request, func(*fwcontext.Request) *fwcontext.Response {
							if got := writer.Header().Get(field.header); got != want {
								t.Errorf("before downstream: %s=%q, want %q", field.header, got, want)
							}
							switch mode {
							case "nil":
								return nil
							case "early":
								writer.WriteHeader(http.StatusNoContent)
								return nil
							case "stream":
								if _, err := writer.Write([]byte("chunk")); err != nil {
									t.Fatal(err)
								}
								writer.Flush()
								return nil
							case "error":
								return fwcontext.NewResponse().Code(http.StatusInternalServerError).Header(field.header, "invalid")
							default:
								return fwcontext.NewResponse().Header(field.header, "invalid").Content("ok")
							}
						})
						if response == nil || response.Headers().Get(field.header) != want {
							t.Fatalf("framework response did not preserve canonical %s=%q", field.header, want)
						}
						result := writer.Result()
						defer result.Body.Close()
						if got := result.Header.Get(field.header); got != want {
							t.Errorf("recorded response: %s=%q, want %q", field.header, got, want)
						}
						if mode == "early" && result.StatusCode != http.StatusNoContent {
							t.Errorf("early status changed: %d", result.StatusCode)
						}
						if mode == "stream" && (!writer.Flushed || writer.Body.String() != "chunk") {
							t.Error("streaming response was changed")
						}
					})
				}
			}
		}
	}
}

func TestSecurityHeadersCanonicalizationPreservesPoliciesAndSnapshot(t *testing.T) {
	config := SecurityHeadersConfig{
		ContentSecurityPolicy:     "script-src 'nonce-AbCdEf0123' https://CDN.example/CaseSensitive; object-src 'none'",
		PermissionsPolicy:         `camera=("https://Example.test/CaseSensitive"), geolocation=()`,
		ReferrerPolicy:            "No-Referrer",
		CrossOriginOpenerPolicy:   "Same-Origin",
		CrossOriginResourcePolicy: "Same-Site",
		FrameOptions:              "sameorigin",
		HSTSMaxAgeSeconds:         31536000,
		HSTSIncludeSubDomains:     true,
		HSTSPreload:               true,
	}
	original := config
	handler, err := NewSecurityHeaders(config)
	if err != nil {
		t.Fatal(err)
	}
	if config != original {
		t.Fatal("constructor changed caller configuration")
	}
	config.CrossOriginResourcePolicy = "cross-origin"
	config.ContentSecurityPolicy = "default-src *"
	for i := 0; i < 2; i++ {
		writer := httptest.NewRecorder()
		request, err := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "https://example.com/", nil), fwcontext.WithResponseWriter(writer))
		if err != nil {
			t.Fatal(err)
		}
		response := handler(request, func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse() })
		for name, want := range map[string]string{
			"Content-Security-Policy":      original.ContentSecurityPolicy,
			"Permissions-Policy":           original.PermissionsPolicy,
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-site",
			"X-Frame-Options":              "SAMEORIGIN",
			"Strict-Transport-Security":    "max-age=31536000; includeSubDomains; preload",
		} {
			if response.Headers().Get(name) != want || writer.Header().Get(name) != want {
				t.Errorf("request %d: %s was changed or not canonicalized", i, name)
			}
		}
		response.Headers().Set("Cross-Origin-Resource-Policy", "cross-origin")
		writer.Header().Set("Content-Security-Policy", "default-src *")
	}
}

func TestSecurityHeadersCanonicalizationPreservesValidation(t *testing.T) {
	for _, config := range []SecurityHeadersConfig{
		{CrossOriginResourcePolicy: " SAME-ORIGIN"},
		{CrossOriginResourcePolicy: "SAME-ORIGIN "},
		{CrossOriginResourcePolicy: "SAME-ORIGIN\r\nX-Evil: yes"},
		{CrossOriginOpenerPolicy: "SAME-ORIGIN\x00"},
		{CrossOriginOpenerPolicy: "unknown"},
		{ReferrerPolicy: "NO-REFERRER "},
		{ReferrerPolicy: "unsafe-url"},
		{FrameOptions: " ALLOWALL"},
		{ContentSecurityPolicy: strings.Repeat("x", maximumSecurityPolicyBytes+1)},
		{PermissionsPolicy: "camera=()\nX-Evil: yes"},
		{HSTSMaxAgeSeconds: -1},
		{HSTSMaxAgeSeconds: 100, HSTSPreload: true},
		{HSTSMaxAgeSeconds: 31536000, HSTSPreload: true},
	} {
		handler, err := NewSecurityHeaders(config)
		if handler != nil || !errors.Is(err, ErrInvalidSecurityHeaders) {
			t.Errorf("invalid configuration returned handler nil=%v, error=%v: %+v", handler == nil, err, config)
		}
	}
	defaults := DefaultSecurityHeadersConfig()
	for _, config := range []SecurityHeadersConfig{{}, {
		ReferrerPolicy: " \t", CrossOriginOpenerPolicy: " \t", CrossOriginResourcePolicy: " \t", FrameOptions: " \t",
	}} {
		handler, err := NewSecurityHeaders(config)
		if err != nil {
			t.Fatal(err)
		}
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
		response := handler(request, func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse() })
		for name, want := range map[string]string{
			"X-Content-Type-Options": "nosniff", "X-Frame-Options": defaults.FrameOptions,
			"Referrer-Policy": defaults.ReferrerPolicy, "Cross-Origin-Opener-Policy": defaults.CrossOriginOpenerPolicy,
			"Cross-Origin-Resource-Policy": defaults.CrossOriginResourcePolicy,
			"Content-Security-Policy":      defaults.ContentSecurityPolicy, "Permissions-Policy": defaults.PermissionsPolicy,
		} {
			if got := response.Headers().Get(name); got != want {
				t.Errorf("default %s=%q, want %q", name, got, want)
			}
		}
		if response.Headers().Get("Strict-Transport-Security") != "" {
			t.Error("canonicalization enabled HSTS on HTTP")
		}
	}
}
