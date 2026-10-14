package http

import (
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
)

func TestBrowserSecurityDefaultsOnHTTPResponseBoundaries(t *testing.T) {
	base := t.TempDir()
	ensureHTTPTestConfigFiles(t, base)
	files := map[string]map[string]interface{}{
		"app":     {"app_env": "test", "app_debug": false, "security_profile": "browser_cookie", "security_strict": true, "session_enable": true, "database_startup_policy": "disabled", "server": map[string]interface{}{"allowed_hosts": []string{"example.test"}}, "compression": map[string]interface{}{"enable": false}},
		"cookie":  {"secret": strings.Repeat("k", 32), "secure": true, "httponly": true, "samesite": "Lax"},
		"session": {"type": "memory", "name": "SID", "reject_unknown_id": true, "samesite": "Lax"},
		"csrf":    {"secure": true},
		"cors":    {"enable": true, "allow_origins": []string{"https://client.example"}, "allow_methods": []string{"GET", "POST", "OPTIONS"}, "allow_headers": []string{"X-CSRF-Token", "Content-Type"}},
	}
	for name, values := range files {
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "config", name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(base, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "public", "asset.txt"), []byte("asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := framework.NewAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	app.Route().Get("/page", func(req *fwcontext.Request) *fwcontext.Response {
		if err := req.GetData("_session").(*session.Session).Set("cart", "item"); err != nil {
			t.Error(err)
		}
		return fwcontext.NewResponse().Content("page")
	})
	posts := 0
	app.Route().Post("/page", func(*fwcontext.Request) *fwcontext.Response { posts++; return fwcontext.NewResponse().Content("saved") })
	app.Route().Get("/error", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Code(stdhttp.StatusBadRequest).Content("bad request")
	})
	app.Route().Get("/panic", func(*fwcontext.Request) *fwcontext.Response { panic("test-only recovery") })
	app.Route().Get("/stream", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		if err := stdhttp.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		if _, err := io.WriteString(w, "stream"); err != nil {
			t.Error(err)
		}
	}))
	kernel, err := NewHttp(app)
	if err != nil {
		t.Fatal(err)
	}
	var csrf, sid *stdhttp.Cookie
	for _, test := range []struct {
		path   string
		status int
		body   string
	}{
		{"/page", stdhttp.StatusOK, "page"},
		{"/error", stdhttp.StatusBadRequest, "bad request"},
		{"/panic", stdhttp.StatusInternalServerError, ""},
		{"/asset.txt", stdhttp.StatusOK, "asset"},
		{"/stream", stdhttp.StatusOK, "stream"},
	} {
		t.Run(test.path, func(t *testing.T) {
			writer := httptest.NewRecorder()
			kernel.ServeHTTP(writer, httptest.NewRequest(stdhttp.MethodGet, "https://example.test"+test.path, nil))
			response := writer.Result()
			defer response.Body.Close()
			if response.StatusCode != test.status || test.body != "" && writer.Body.String() != test.body {
				t.Fatalf("status=%d body=%q", response.StatusCode, writer.Body.String())
			}
			assertDefaultBrowserHeaders(t, response.Header)
			if test.path == "/page" {
				for _, c := range response.Cookies() {
					if c.Name == middleware.DefaultCSRFCookieName {
						csrf = c
					}
					if c.Name == "SID" {
						sid = c
					}
				}
			}
		})
	}
	if csrf == nil || sid == nil || !sid.HttpOnly || !sid.Secure || sid.SameSite != stdhttp.SameSiteLaxMode {
		t.Fatal("browser defaults did not send protected session/CSRF cookies")
	}
	for _, includeToken := range []bool{false, true} {
		raw := httptest.NewRequest(stdhttp.MethodPost, "https://example.test/page", nil)
		raw.Header.Set("Origin", "https://example.test")
		raw.AddCookie(sid)
		raw.AddCookie(csrf)
		if includeToken {
			raw.Header.Set(middleware.DefaultCSRFHeaderName, csrf.Value)
		}
		writer := httptest.NewRecorder()
		kernel.ServeHTTP(writer, raw)
		want := stdhttp.StatusForbidden
		if includeToken {
			want = stdhttp.StatusOK
		}
		if writer.Code != want {
			t.Fatalf("csrf=%t status=%d body=%q", includeToken, writer.Code, writer.Body.String())
		}
		assertDefaultBrowserHeaders(t, writer.Result().Header)
	}
	if posts != 1 {
		t.Fatalf("CSRF allowed %d state-changing handlers", posts)
	}
	for _, origin := range []string{"https://client.example", "https://untrusted.example"} {
		raw := httptest.NewRequest(stdhttp.MethodOptions, "https://example.test/page", nil)
		raw.Header.Set("Origin", origin)
		raw.Header.Set("Access-Control-Request-Method", stdhttp.MethodPost)
		raw.Header.Set("Access-Control-Request-Headers", middleware.DefaultCSRFHeaderName)
		writer := httptest.NewRecorder()
		kernel.ServeHTTP(writer, raw)
		want := stdhttp.StatusNoContent
		if origin != "https://client.example" {
			want = stdhttp.StatusForbidden
		}
		if writer.Code != want {
			t.Fatalf("CORS origin=%s status=%d", origin, writer.Code)
		}
		assertDefaultBrowserHeaders(t, writer.Result().Header)
	}
}

func assertDefaultBrowserHeaders(t *testing.T, header stdhttp.Header) {
	t.Helper()
	if header.Get("X-Content-Type-Options") != "nosniff" || header.Get("X-Frame-Options") != "DENY" || header.Get("Content-Security-Policy") != middleware.DefaultSecurityHeadersConfig().ContentSecurityPolicy {
		t.Fatal("default browser headers missing at HTTP boundary")
	}
}
