package framework

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

func TestBrowserSecurityProfileDefaultsAndExplicitCSRF(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		session       bool
		csrf          interface{}
		want          bool
	}{
		{"explicit-browser", "browser_cookie", true, nil, true},
		{"inferred-browser", "", true, nil, true},
		{"stateless", "stateless_api", false, nil, false},
		{"legacy-empty", "", false, nil, false},
		{"explicit-optout", "browser_cookie", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{config: config.NewConfig()}
			setStrictSecurityValue(t, app, "app.session_enable", tc.session)
			if tc.profile != "" {
				setStrictSecurityValue(t, app, "app.security_profile", tc.profile)
			}
			if tc.csrf != nil {
				setStrictSecurityValue(t, app, "app.csrf_enable", tc.csrf)
			}
			app.applyApplicationRuntimeConfig()
			if app.csrfEnabled != tc.want || app.StartupError() != nil {
				t.Fatalf("csrf=%t want=%t error=%v", app.csrfEnabled, tc.want, app.StartupError())
			}
		})
	}
}

func writeBrowserSecurityConfig(t *testing.T, strict bool, overrides map[string]interface{}) *App {
	t.Helper()
	base := t.TempDir()
	writeTestAppConfigFiles(t, base)
	files := map[string]map[string]interface{}{
		"app":     {"app_env": "test", "app_debug": false, "security_profile": "browser_cookie", "security_strict": strict, "session_enable": true, "database_startup_policy": "disabled", "server": map[string]interface{}{"allowed_hosts": []string{"example.test"}}},
		"cookie":  {"secret": strings.Repeat("k", 32), "secure": true, "httponly": true, "samesite": "Lax"},
		"csrf":    {"secure": true},
		"session": {"type": "memory", "reject_unknown_id": true},
	}
	for path, value := range overrides {
		parts := strings.SplitN(path, ".", 2)
		if len(parts) != 2 {
			t.Fatal("expected section.field override")
		}
		if files[parts[0]] == nil {
			files[parts[0]] = map[string]interface{}{}
		}
		files[parts[0]][parts[1]] = value
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
	app := NewAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func TestBrowserSecurityDefaultsReachAssembledResponses(t *testing.T) {
	app := writeBrowserSecurityConfig(t, true, nil)
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"framework", "error", "static", "stream"} {
		t.Run(mode, func(t *testing.T) {
			writer := httptest.NewRecorder()
			req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://example.test/page", nil), fwcontext.WithResponseWriter(writer))
			response := app.Middleware().Then(req, func(*fwcontext.Request) *fwcontext.Response {
				if mode == "stream" {
					writer.Flush()
					_, _ = writer.WriteString("stream")
				}
				if mode == "static" {
					writer.WriteHeader(http.StatusOK)
					_, _ = writer.WriteString("asset")
				}
				if mode == "error" {
					return fwcontext.NewResponse().Code(http.StatusBadRequest).Content("bad request")
				}
				return fwcontext.NewResponse().Content("page")
			})
			if response.Headers().Get("X-Content-Type-Options") != "nosniff" || response.Headers().Get("X-Frame-Options") != "DENY" || response.Headers().Get("Content-Security-Policy") == "" {
				t.Fatal("default browser headers missing from response")
			}
			if mode == "stream" || mode == "static" {
				if writer.Result().Header.Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("headers missing before direct write")
				}
			}
		})
	}
	get := app.Middleware().Then(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://example.test/page", nil)), func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("page") })
	var token *http.Cookie
	for _, c := range (&http.Response{Header: get.Headers()}).Cookies() {
		if c.Name == middleware.DefaultCSRFCookieName {
			token = c
		}
	}
	if token == nil {
		t.Fatal("default browser CSRF did not issue token")
	}
	for _, includeToken := range []bool{false, true} {
		raw := httptest.NewRequest(http.MethodPost, "https://example.test/save", nil)
		raw.AddCookie(token)
		raw.Header.Set("Origin", "https://example.test")
		if includeToken {
			raw.Header.Set(middleware.DefaultCSRFHeaderName, token.Value)
		}
		called := false
		response := app.Middleware().Then(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
			called = true
			return fwcontext.NewResponse().Content("saved")
		})
		want := http.StatusForbidden
		if includeToken {
			want = http.StatusOK
		}
		if response.GetStatus() != want || called != includeToken || response.Headers().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("CSRF token=%t status=%d called=%t", includeToken, response.GetStatus(), called)
		}
	}
}

func TestStrictBrowserSecurityRejectsPolicyDowngrades(t *testing.T) {
	for path, value := range map[string]interface{}{"cookie.httponly": false, "cookie.samesite": "", "session.samesite": "None", "session.reject_unknown_id": false, "app.csrf_enable": false} {
		t.Run(path, func(t *testing.T) {
			app := writeBrowserSecurityConfig(t, true, map[string]interface{}{path: value})
			if err := app.Initialize(); err == nil || app.State() != ApplicationStateFailed {
				t.Fatalf("unsafe browser policy initialized: %v", err)
			}
			if lease, err := app.AcquireRunLease(); lease != nil || err == nil {
				t.Fatal("unsafe browser policy started")
			}
		})
	}
}

func TestBrowserSecurityExplicitOptOutRemainsAvailableWithoutStrictMode(t *testing.T) {
	app := writeBrowserSecurityConfig(t, false, map[string]interface{}{"app.csrf_enable": false, "security_headers.enable": false})
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	called := false
	response := app.Middleware().Then(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodPost, "https://example.test/save", nil)), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("ok")
	})
	if !called || response.Headers().Get("X-Content-Type-Options") != "" {
		t.Fatal("explicit opt-out ignored")
	}
}

func TestBrowserSecurityDefaultsDoNotLeakIntoSiblingAPI(t *testing.T) {
	base := t.TempDir()
	writeTestAppConfigFiles(t, base)
	root := []byte(`{"app_env":"test","app_debug":false,"session_enable":false,"database_startup_policy":"disabled"}`)
	if err := os.WriteFile(filepath.Join(base, "config", "app.json"), root, 0o600); err != nil {
		t.Fatal(err)
	}
	writeNativeApplicationConfigValues(t, base, "index", map[string]interface{}{"security_profile": "browser_cookie", "session_enable": true})
	writeNativeApplicationConfigValues(t, base, "api", map[string]interface{}{"security_profile": "stateless_api", "session_enable": false})
	project := NewAppUninitialized(base)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(func(*App) error { return nil }, ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }}, ApplicationDefinition{Name: "api", Register: func(*App) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if err := project.Initialize(); err != nil {
		t.Fatal(err)
	}
	if !project.csrfEnabled || project.securityProfile != SecurityProfileBrowserCookie {
		t.Fatal("primary browser defaults missing")
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatal(err)
	}
	api := applications["api"]
	if err := api.Initialize(); err != nil {
		t.Fatal(err)
	}
	if api.csrfEnabled || api.securityProfile != SecurityProfileStatelessAPI {
		t.Fatal("primary browser state leaked into API sibling")
	}
	if _, exists := project.ProjectConfig()["app"].(map[string]interface{})["csrf_enable"]; exists {
		t.Fatal("runtime default mutated frozen project configuration")
	}
	called := false
	response := api.Middleware().Then(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodPost, "https://example.test/api", nil)), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("ok")
	})
	if !called || response.GetStatus() != http.StatusOK || response.Headers().Get("X-Content-Type-Options") != "" || len(response.Headers().Values("Set-Cookie")) != 0 {
		t.Fatal("API sibling inherited browser middleware")
	}
}
