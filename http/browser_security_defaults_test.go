package http

import (
	"encoding/json"
	"fmt"
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
	testBrowserSecurityHTTPBoundaries(t, nil, false, "")
}

func TestBrowserSecurityHeadersIgnoreMiddlewarePriority(t *testing.T) {
	for _, first := range []string{"cors", "rate_limit", "csrf", "recovery"} {
		for _, duplicate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duplicate=%t", first, duplicate), func(t *testing.T) {
				priority := []string{first}
				for _, name := range []string{"cors", "rate_limit", "csrf", "recovery", "security_headers"} {
					if name != first {
						priority = append(priority, name)
					}
				}
				testBrowserSecurityHTTPBoundaries(t, priority, duplicate, "")
			})
		}
	}
}

func TestBrowserSecurityHeadersSurviveMiddlewarePanicBeforeRecovery(t *testing.T) {
	for _, mode := range []string{"unshift", "priority"} {
		t.Run(mode, func(t *testing.T) { testBrowserSecurityHTTPBoundaries(t, nil, false, mode) })
	}
}

func testBrowserSecurityHTTPBoundaries(t *testing.T, priority []string, duplicate bool, earlyPanic string) {
	t.Helper()
	base := t.TempDir()
	ensureHTTPTestConfigFiles(t, base)
	files := map[string]map[string]interface{}{
		"app":        {"app_env": "test", "app_debug": false, "security_profile": "browser_cookie", "security_strict": true, "session_enable": true, "database_startup_policy": "disabled", "server": map[string]interface{}{"allowed_hosts": []string{"example.test"}}, "compression": map[string]interface{}{"enable": false}},
		"cookie":     {"secret": strings.Repeat("k", 32), "secure": true, "httponly": true, "samesite": "Lax"},
		"session":    {"type": "memory", "name": "SID", "reject_unknown_id": true, "samesite": "Lax"},
		"csrf":       {"secure": true},
		"cors":       {"enable": true, "allow_origins": []string{"https://client.example"}, "allow_methods": []string{"GET", "POST", "OPTIONS"}, "allow_headers": []string{"X-CSRF-Token", "Content-Type"}},
		"rate_limit": {"enable": true, "rate": 1, "period_seconds": 3600, "burst": 1},
	}
	if priority != nil {
		files["middleware"] = map[string]interface{}{"priority": priority}
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
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	if duplicate {
		if err := app.Middleware().PipeByNameStrict("security_headers"); err != nil {
			t.Fatal(err)
		}
		if err := app.Middleware().PipeByNameStrict("security_headers"); err != nil {
			t.Fatal(err)
		}
	}
	if earlyPanic != "" {
		handler := func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			if req.Raw().URL.Path == "/early-panic" {
				panic("test-only middleware before ordinary recovery")
			}
			return next(req)
		}
		if earlyPanic == "unshift" {
			app.Middleware().Unshift(handler)
		} else {
			app.Middleware().Alias("early_panic", handler)
			if err := app.Middleware().PipeByNameStrict("early_panic"); err != nil {
				t.Fatal(err)
			}
			app.Middleware().SetPriority([]string{"early_panic", "cors", "rate_limit", "csrf", "recovery", "security_headers"})
		}
	}
	kernel, err := NewHttp(app)
	if err != nil {
		t.Fatal(err)
	}
	var csrf, sid *stdhttp.Cookie
	requestIndex := 0
	newRequest := func(method, path string) *stdhttp.Request {
		requestIndex++
		raw := httptest.NewRequest(method, "https://example.test"+path, nil)
		raw.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", requestIndex)
		return raw
	}
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
			kernel.ServeHTTP(writer, newRequest(stdhttp.MethodGet, test.path))
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
	if earlyPanic != "" {
		writer := httptest.NewRecorder()
		kernel.ServeHTTP(writer, newRequest(stdhttp.MethodGet, "/early-panic"))
		if writer.Code != stdhttp.StatusInternalServerError {
			t.Fatalf("early middleware panic status=%d", writer.Code)
		}
		assertDefaultBrowserHeaders(t, writer.Result().Header)
		// Direct Run has no prewritten Writer headers to retain during recovery.
		response := kernel.Run(fwcontext.MustNewRequest(newRequest(stdhttp.MethodGet, "/early-panic")))
		kernel.End(response)
		if response.GetStatus() != stdhttp.StatusInternalServerError {
			t.Fatalf("direct early middleware panic status=%d", response.GetStatus())
		}
		assertDefaultBrowserHeaders(t, response.Headers())
	}
	for _, includeToken := range []bool{false, true} {
		raw := newRequest(stdhttp.MethodPost, "/page")
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
		raw := newRequest(stdhttp.MethodOptions, "/page")
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
	for _, want := range []int{stdhttp.StatusBadRequest, stdhttp.StatusTooManyRequests} {
		raw := newRequest(stdhttp.MethodGet, "/error")
		raw.RemoteAddr = "198.51.100.77:1234"
		writer := httptest.NewRecorder()
		kernel.ServeHTTP(writer, raw)
		if writer.Code != want {
			t.Fatalf("rate-limit boundary status=%d want=%d", writer.Code, want)
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
