package cookie_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

func assertSameSiteHeader(t *testing.T, header string, mode http.SameSite, secure bool) {
	t.Helper()
	values := (&http.Response{Header: http.Header{"Set-Cookie": {header}}}).Cookies()
	parsedMode := mode
	if mode == http.SameSiteDefaultMode {
		// No SameSite attribute leaves the parsed Go field at its zero value.
		parsedMode = 0
	}
	if len(values) != 1 || values[0].SameSite != parsedMode || values[0].Secure != secure {
		t.Fatalf("header=%q, want SameSite=%d Secure=%t", header, mode, secure)
	}
	if mode == http.SameSiteDefaultMode && strings.Contains(header, "SameSite=") {
		t.Fatalf("default mode unexpectedly serialized: %q", header)
	}
}

func TestSameSiteNormalizationAcrossConstructors(t *testing.T) {
	cases := []struct {
		value string
		mode  http.SameSite
	}{
		{"", http.SameSiteDefaultMode},
		{" \u2003 ", http.SameSiteDefaultMode},
		{"Strict", http.SameSiteStrictMode},
		{" sTrIcT ", http.SameSiteStrictMode},
		{"\u2003Strict\u00a0", http.SameSiteStrictMode},
		{" LaX ", http.SameSiteLaxMode},
		{" NoNe ", http.SameSiteNoneMode},
		{"\u2003NONE\u00a0", http.SameSiteNoneMode},
	}
	for _, test := range cases {
		t.Run(test.value, func(t *testing.T) {
			config := cookie.DefaultConfig()
			config.SameSite = test.value
			constructors := map[string]func() (*cookie.Cookie, error){
				"map": func() (*cookie.Cookie, error) {
					return cookie.NewCookie(map[string]interface{}{"samesite": test.value})
				},
				"typed": func() (*cookie.Cookie, error) { return cookie.NewCookieWithConfig(config) },
				"request": func() (*cookie.Cookie, error) {
					return cookie.NewCookieForRequest(config, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), httptest.NewRecorder())
				},
				"trusted-request": func() (*cookie.Cookie, error) {
					return cookie.NewCookieForRequestWithSecure(config, httptest.NewRequest(http.MethodGet, "https://example.test/", nil), httptest.NewRecorder(), false)
				},
				"derived-request": func() (*cookie.Cookie, error) {
					factory, err := cookie.NewCookieWithConfig(config)
					if err != nil {
						return nil, err
					}
					return factory.ForRequest(httptest.NewRequest(http.MethodGet, "http://example.test/", nil), httptest.NewRecorder())
				},
				"derived-trusted-request": func() (*cookie.Cookie, error) {
					factory, err := cookie.NewCookieWithConfig(config)
					if err != nil {
						return nil, err
					}
					return factory.ForRequestWithSecure(httptest.NewRequest(http.MethodGet, "https://example.test/", nil), httptest.NewRecorder(), false)
				},
			}
			for name, construct := range constructors {
				t.Run(name, func(t *testing.T) {
					current, err := construct()
					if err != nil {
						t.Fatal(err)
					}
					header, err := current.BuildHeader("policy", "test-only")
					if err != nil {
						t.Fatal(err)
					}
					assertSameSiteHeader(t, header, test.mode, test.mode == http.SameSiteNoneMode)
				})
			}
			if config.SameSite != test.value {
				t.Fatal("constructor mutated caller configuration")
			}
		})
	}
}

func TestSameSiteNormalizationOverridesAndSecurePolicy(t *testing.T) {
	for _, policy := range []struct {
		value string
		mode  http.SameSite
	}{
		{"", http.SameSiteStrictMode}, // A zero option inherits the factory.
		{" \u2003 ", http.SameSiteDefaultMode},
		{" Strict ", http.SameSiteStrictMode},
		{" LaX ", http.SameSiteLaxMode},
		{" \u00a0nOnE\u2003 ", http.SameSiteNoneMode},
	} {
		for _, configuredSecure := range []bool{false, true} {
			for _, trustedSecure := range []bool{false, true} {
				config := cookie.DefaultConfig()
				config.SameSite, config.Secure, config.HttpOnly = "Strict", configuredSecure, true
				writer := httptest.NewRecorder()
				current, err := cookie.NewCookieForRequestWithSecure(config, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), writer, trustedSecure)
				if err != nil {
					t.Fatal(err)
				}
				if err := current.Set("policy", "test-only", cookie.CookieOptions{SameSite: policy.value, Expire: 120}); err != nil {
					t.Fatal(err)
				}
				headers := writer.Header().Values("Set-Cookie")
				if len(headers) != 1 {
					t.Fatalf("header count=%d", len(headers))
				}
				assertSameSiteHeader(t, headers[0], policy.mode, configuredSecure || trustedSecure || policy.mode == http.SameSiteNoneMode)
				got := (&http.Response{Header: writer.Header()}).Cookies()[0]
				if !got.HttpOnly || got.MaxAge != 120 || got.Path != "/" || got.Domain != "" {
					t.Fatal("unrelated cookie policy changed")
				}
			}
		}
	}
}

func TestSameSiteNormalizationDeletePreservesPolicy(t *testing.T) {
	for _, test := range []struct {
		value string
		mode  http.SameSite
	}{{" Strict ", http.SameSiteStrictMode}, {" None ", http.SameSiteNoneMode}} {
		config := cookie.DefaultConfig()
		config.SameSite, config.HttpOnly = test.value, true
		writer := httptest.NewRecorder()
		current, err := cookie.NewCookieForRequestWithSecure(config, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), writer, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := current.Delete("policy"); err != nil {
			t.Fatal(err)
		}
		headers := writer.Header().Values("Set-Cookie")
		if len(headers) != 1 {
			t.Fatalf("header count=%d", len(headers))
		}
		assertSameSiteHeader(t, headers[0], test.mode, test.mode == http.SameSiteNoneMode)
		got := (&http.Response{Header: writer.Header()}).Cookies()[0]
		if got.MaxAge != -1 || got.Value != "" || !got.HttpOnly {
			t.Fatal("deletion contract changed")
		}
	}
}

func TestSameSiteNormalizationInvalidInputDoesNotWrite(t *testing.T) {
	for _, value := range []string{"Strict; None", "Str ict", "NoneX", "\x00Strict", "No\nne"} {
		config := cookie.DefaultConfig()
		config.SameSite = value
		if current, err := cookie.NewCookieWithConfig(config); current != nil || !errors.Is(err, cookie.ErrInvalidCookieConfig) {
			t.Fatalf("invalid typed policy accepted: %q %v", value, err)
		}
		writer := httptest.NewRecorder()
		current, err := cookie.NewCookieForRequest(cookie.DefaultConfig(), httptest.NewRequest(http.MethodGet, "http://example.test/", nil), writer)
		if err != nil {
			t.Fatal(err)
		}
		if err := current.Set("policy", "test-only", cookie.CookieOptions{SameSite: value}); !errors.Is(err, cookie.ErrInvalidCookieOptions) {
			t.Fatalf("invalid override accepted: %q %v", value, err)
		}
		if len(writer.Header().Values("Set-Cookie")) != 0 {
			t.Fatal("invalid override wrote a cookie")
		}
	}
}

func FuzzSameSiteNormalizationPolicy(f *testing.F) {
	f.Add(" ", " ", uint8(0))
	f.Add("\u2003", "\u00a0", uint8(2))
	f.Add("\t", "\r\n", uint8(1))
	f.Fuzz(func(t *testing.T, prefix, suffix string, selector uint8) {
		if len(prefix)+len(suffix) > 128 || strings.TrimSpace(prefix) != "" || strings.TrimSpace(suffix) != "" {
			return
		}
		modes := []http.SameSite{http.SameSiteStrictMode, http.SameSiteLaxMode, http.SameSiteNoneMode, http.SameSiteDefaultMode}
		names := []string{"sTrIcT", "LaX", "nOnE", ""}
		index := int(selector % 4)
		value := prefix + names[index] + suffix
		config := cookie.DefaultConfig()
		config.SameSite = value
		current, err := cookie.NewCookieWithConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		header, err := current.BuildHeader("policy", "test-only")
		if err != nil {
			t.Fatal(err)
		}
		assertSameSiteHeader(t, header, modes[index], modes[index] == http.SameSiteNoneMode)
		base, err := cookie.NewCookie(nil)
		if err != nil {
			t.Fatal(err)
		}
		header, err = base.BuildHeader("policy", "test-only", cookie.CookieOptions{SameSite: value})
		if err != nil {
			t.Fatal(err)
		}
		assertSameSiteHeader(t, header, modes[index], modes[index] == http.SameSiteNoneMode)
	})
}
