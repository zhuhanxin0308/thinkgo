package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

func newRateLimitWarningApp(t *testing.T, environment string, profile framework.SecurityProfile, policy map[string]interface{}) *framework.App {
	t.Helper()
	base := t.TempDir()
	ensureHTTPTestConfigFiles(t, base)
	browser := profile == framework.SecurityProfileBrowserCookie
	files := map[string]interface{}{
		"app.json": map[string]interface{}{
			"app_env": environment, "app_debug": false, "security_profile": profile,
			"session_enable": browser, "csrf_enable": browser,
			"server":      map[string]interface{}{"host": "127.0.0.1", "port": 8080, "allowed_hosts": []string{"example.com"}},
			"compression": map[string]interface{}{"enable": false},
		},
		"cookie.json": map[string]interface{}{"secret": "issue38-test-cookie-secret-32-characters", "secure": true},
		"csrf.json":   map[string]interface{}{"secret": "issue38-test-csrf-secret-32-characters", "secure": true},
	}
	if environment == "" {
		delete(files["app.json"].(map[string]interface{}), "app_env")
	}
	if policy != nil {
		files["rate_limit.json"] = policy
	}
	for name, value := range files {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "config", name), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	app := framework.NewAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Initialize(); err != nil {
		t.Fatalf("valid production fixture could not initialize: %v", err)
	}
	return app
}

func rateLimitWarningCount(t *testing.T, logger *frameworklog.Log, driver *accessLogDriver) int {
	t.Helper()
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range driver.allEntries() {
		if entry.Message == productionRateLimitWarningMessage {
			count++
			if entry.Level != frameworklog.LevelWarning || entry.Context["code"] != "rate_limit_disabled" || entry.Context["configuration"] != "rate_limit.enable" {
				t.Fatalf("missing stable warning metadata: %#v", entry.Context)
			}
		}
	}
	return count
}

func TestProductionRateLimitWarningPreservesHTTPBehavior(t *testing.T) {
	for _, profile := range []framework.SecurityProfile{framework.SecurityProfileStatelessAPI, framework.SecurityProfileBrowserCookie} {
		for _, choice := range []struct {
			name    string
			policy  map[string]interface{}
			warning int
			limited bool
		}{
			{"missing", nil, 1, false},
			{"disabled", map[string]interface{}{"enable": false, "rate": 1, "burst": 1}, 1, false},
			{"enabled", map[string]interface{}{"enable": true, "rate": 1, "burst": 1, "period_seconds": 3600}, 0, true},
		} {
			t.Run(string(profile)+"/"+choice.name, func(t *testing.T) {
				app := newRateLimitWarningApp(t, "production", profile, choice.policy)
				driver := &accessLogDriver{}
				logger := frameworklog.NewLog(driver)
				t.Cleanup(func() { _ = logger.Close() })
				if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
					t.Fatal(err)
				}
				calls := 0
				if _, err := mustHTTPRoute(t, app).Get("/rate-warning", func(*fwcontext.Request) *fwcontext.Response {
					calls++
					return fwcontext.NewResponse().Content("ok")
				}); err != nil {
					t.Fatal(err)
				}
				handler := newTestHTTPHandler(t, app)
				for index := range 3 {
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "https://example.com/rate-warning", nil))
					want := stdhttp.StatusOK
					if choice.limited && index > 0 {
						want = stdhttp.StatusTooManyRequests
					}
					if recorder.Code != want {
						t.Fatalf("request %d status=%d want=%d body=%q", index, recorder.Code, want, recorder.Body.String())
					}
				}
				wantCalls := 3
				if choice.limited {
					wantCalls = 1
				}
				if calls != wantCalls || app.StartupError() != nil {
					t.Fatalf("advisory changed execution: calls=%d error=%v", calls, app.StartupError())
				}
				if count := rateLimitWarningCount(t, logger, driver); count != choice.warning {
					t.Fatalf("warnings=%d want=%d", count, choice.warning)
				}
			})
		}
	}
}

func TestProductionRateLimitWarningEnvironmentAndDedup(t *testing.T) {
	for _, sample := range []struct {
		environment string
		want        int
	}{{" prod ", 1}, {"RELEASE", 1}, {"Production", 1}, {"test", 0}, {"development", 0}, {"", 0}} {
		t.Run(sample.environment, func(t *testing.T) {
			app := newRateLimitWarningApp(t, sample.environment, framework.SecurityProfileStatelessAPI, nil)
			driver := &accessLogDriver{}
			logger := frameworklog.NewLog(driver)
			t.Cleanup(func() { _ = logger.Close() })
			handler := &Http{app: app, log: logger}
			var wg sync.WaitGroup
			for range 32 {
				wg.Go(handler.warnProductionRateLimit)
			}
			wg.Wait()
			if count := rateLimitWarningCount(t, logger, driver); count != sample.want {
				t.Fatalf("warnings=%d want=%d", count, sample.want)
			}
		})
	}
}

func TestProductionRateLimitWarningUsesFinalLogger(t *testing.T) {
	app := newRateLimitWarningApp(t, "production", framework.SecurityProfileStatelessAPI, nil)
	oldDriver, finalDriver := &accessLogDriver{}, &accessLogDriver{}
	oldLog, finalLog := frameworklog.NewLog(oldDriver), frameworklog.NewLog(finalDriver)
	t.Cleanup(func() { _ = oldLog.Close(); _ = finalLog.Close() })
	if err := app.Instance(string(framework.ServiceLog), oldLog); err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, app)
	if err := handler.ensureInitialized(); err != nil {
		t.Fatal(err)
	}
	if count := rateLimitWarningCount(t, oldLog, oldDriver); count != 1 {
		t.Fatalf("initial warnings=%d", count)
	}
	if err := app.Instance(string(framework.ServiceLog), finalLog); err != nil {
		t.Fatal(err)
	}
	leases, err := handler.acquireApplicationRunLeases()
	if err != nil {
		t.Fatal(err)
	}
	defer leases.Release()
	for range 3 {
		if err := handler.reloadApplicationRuntimeServices(); err != nil {
			t.Fatal(err)
		}
	}
	if count := rateLimitWarningCount(t, finalLog, finalDriver); count != 1 {
		t.Fatalf("final runtime logger warnings=%d", count)
	}
	if count := rateLimitWarningCount(t, oldLog, oldDriver); count != 1 {
		t.Fatalf("old logger received runtime warning: %d", count)
	}
}

func TestProductionRateLimitWarningMissingDependenciesAndInvalidPolicy(t *testing.T) {
	var absent *Http
	absent.warnProductionRateLimit()
	(&Http{}).warnProductionRateLimit()
	(&Http{app: &framework.App{}}).warnProductionRateLimit()
	app := newRateLimitWarningApp(t, "production", framework.SecurityProfileStatelessAPI, nil)
	driver := &accessLogDriver{}
	logger := frameworklog.NewLog(driver)
	t.Cleanup(func() { _ = logger.Close() })
	(&Http{app: &framework.App{}, log: logger}).warnProductionRateLimit()
	handler := &Http{app: app}
	handler.warnProductionRateLimit()
	handler.log = logger
	app.Config().Set("rate_limit.enable", "not-a-boolean")
	handler.warnProductionRateLimit()
	if count := rateLimitWarningCount(t, logger, driver); count != 0 {
		t.Fatal("invalid configuration was misclassified as disabled")
	}
	app.Config().Set("rate_limit.enable", false)
	handler.warnProductionRateLimit()
	if count := rateLimitWarningCount(t, logger, driver); count != 1 {
		t.Fatal("missing logger or invalid configuration consumed the warning")
	}
}
