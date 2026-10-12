package cookie_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

func sensitivePolicyFactory(t *testing.T) *cookie.Cookie {
	t.Helper()
	factory, err := cookie.NewCookie(map[string]interface{}{"path": "/", "secure": true, "httponly": true, "samesite": "Lax", "expire": 900})
	if err != nil {
		t.Fatal(err)
	}
	return factory
}

func TestSensitiveCookiePolicyResponseHeaders(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			factory := sensitivePolicyFactory(t)
			writer := httptest.NewRecorder()
			raw := httptest.NewRequest(http.MethodGet, scheme+"://example.test/", nil)
			current, err := factory.ForRequestWithSecure(raw, writer, scheme == "https")
			if err != nil {
				t.Fatal(err)
			}
			// Changing only the lifetime must not remove the factory's policy.
			if err := current.Set("auth_token", "test-only-opaque-token", cookie.CookieOptions{Expire: 300}); err != nil {
				t.Fatal(err)
			}
			cookies := (&http.Response{Header: writer.Header()}).Cookies()
			if len(cookies) != 1 {
				t.Fatalf("cookie count=%d, want 1", len(cookies))
			}
			got := cookies[0]
			if got.Name != "auth_token" || got.Value != "test-only-opaque-token" {
				t.Fatal("cookie name or value changed")
			}
			if !got.Secure || !got.HttpOnly || got.SameSite != http.SameSiteLaxMode {
				t.Fatal("explicit sensitive-cookie attributes were lost")
			}
			if got.Path != "/" || got.Domain != "" || got.MaxAge != 300 || got.Expires.IsZero() {
				t.Fatal("cookie scope or explicit lifetime changed")
			}
		})
	}
}

func TestSensitiveCookiePolicyFactoryIsolation(t *testing.T) {
	raw := map[string]interface{}{"secure": true, "httponly": true, "samesite": "Lax"}
	protected, err := cookie.NewCookie(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw["secure"], raw["httponly"], raw["samesite"] = false, false, ""
	copyConfig := protected.GetConfig()
	copyConfig.Secure, copyConfig.HttpOnly = false, false
	public, err := cookie.NewCookieWithConfig(copyConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, factory := range []*cookie.Cookie{protected, public, protected} {
		header, err := factory.BuildHeader("policy_check", "non-sensitive-test-value")
		if err != nil {
			t.Fatal(err)
		}
		cookies := (&http.Response{Header: http.Header{"Set-Cookie": {header}}}).Cookies()
		if len(cookies) != 1 {
			t.Fatal("expected one cookie")
		}
		wantProtected := factory == protected
		if cookies[0].Secure != wantProtected || cookies[0].HttpOnly != wantProtected || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatal("factory policy was changed by input, snapshot or another factory")
		}
	}
}

func TestSensitiveCookiePolicyInvalidOverrideDoesNotWrite(t *testing.T) {
	for _, options := range []cookie.CookieOptions{{Path: "relative"}, {SameSite: "invalid"}, {Expire: -2}} {
		writer := httptest.NewRecorder()
		current, err := sensitivePolicyFactory(t).ForRequest(httptest.NewRequest(http.MethodGet, "https://example.test/", nil), writer)
		if err != nil {
			t.Fatal(err)
		}
		err = current.Set("auth_token", "test-only-opaque-token", options)
		if !errors.Is(err, cookie.ErrInvalidCookieOptions) {
			t.Fatalf("error=%v, want ErrInvalidCookieOptions", err)
		}
		if len(writer.Header().Values("Set-Cookie")) != 0 {
			t.Fatal("invalid policy wrote a response cookie")
		}
	}
}

func ExampleNewCookieWithConfig_sensitivePolicy() {
	config := cookie.DefaultConfig()
	config.Path = "/"
	config.Domain = ""
	config.Secure = true
	config.HttpOnly = true
	config.SameSite = "Lax"
	config.Expire = 900
	factory, err := cookie.NewCookieWithConfig(config)
	if err != nil {
		fmt.Println(err)
		return
	}
	// This value illustrates header construction, not credential generation.
	// Real authentication requires an opaque server-issued and validated token.
	header, err := factory.BuildHeader("auth_token", "example-only-opaque-token")
	if err != nil {
		fmt.Println(err)
		return
	}
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": {header}}}).Cookies()
	if len(cookies) != 1 {
		fmt.Println("invalid cookie header")
		return
	}
	got := cookies[0]
	// Never print the credential value or full Set-Cookie header.
	fmt.Println(got.Secure, got.HttpOnly, got.SameSite == http.SameSiteLaxMode, got.Path, got.Domain == "", got.MaxAge)
	// Output: true true true / true 900
}
