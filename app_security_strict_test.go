package framework

import (
	stdcontext "context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

func setStrictSecurityValue(t *testing.T, app *App, key string, value interface{}) {
	t.Helper()
	if err := app.config.Set(key, value); err != nil {
		t.Fatal(err)
	}
}

func strictSecurityTestApp(t *testing.T, environment string) *App {
	t.Helper()
	app := &App{config: config.NewConfig()}
	setStrictSecurityValue(t, app, "app.app_env", environment)
	setStrictSecurityValue(t, app, "app.security_profile", string(SecurityProfileStatelessAPI))
	return app
}

func TestStrictSecurityActivationDoesNotChangeEnvironment(t *testing.T) {
	for _, environment := range []string{"development", "test", "staging", "production", "prod", " RELEASE "} {
		for _, mode := range []string{"missing", "false", "true"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				app := strictSecurityTestApp(t, environment)
				if mode != "missing" {
					setStrictSecurityValue(t, app, "app.security_strict", mode == "true")
				}
				before := app.Environment()
				app.warnProductionSecurity()
				active := mode == "true" || normalizeSecurityEnvironment(environment) == securityEnvironmentProduction
				if got := app.StartupError(); (got != nil) != active {
					t.Fatalf("active=%t, startup error=%v", active, got)
				} else if active && !strings.Contains(got.Error(), "allowed_hosts") {
					t.Fatalf("missing host check: %v", got)
				}
				if app.Environment() != before || app.IsDebug() {
					t.Fatal("security enforcement changed application metadata")
				}
			})
		}
	}
	var nilApp *App
	nilApp.warnProductionSecurity()
}

func TestStrictSecurityInvalidSwitchFailsClosed(t *testing.T) {
	for _, environment := range []string{"staging", "production"} {
		for _, value := range []interface{}{nil, "", "maybe", 1, 0.0, []string{"true"}, map[string]interface{}{"enabled": true}} {
			app := strictSecurityTestApp(t, environment)
			setStrictSecurityValue(t, app, "app.security_strict", value)
			setStrictSecurityValue(t, app, "app.server.allowed_hosts", []string{"staging.example.test"})
			app.warnProductionSecurity()
			if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "app.security_strict") {
				t.Fatalf("invalid switch %T accepted in %s: %v", value, environment, err)
			}
		}
	}
	// Match the existing strict application-boolean parser, including env strings.
	for _, value := range []string{"true", "1", " TRUE "} {
		app := strictSecurityTestApp(t, "staging")
		setStrictSecurityValue(t, app, "app.security_strict", value)
		app.warnProductionSecurity()
		if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "allowed_hosts") {
			t.Fatalf("valid true switch %q did not enforce hosts: %v", value, err)
		}
	}
}

func TestStrictSecurityReusesProfileChecksAndDeduplication(t *testing.T) {
	for _, check := range []string{"valid-browser", "valid-api", "missing-hosts", "wildcard-hosts", "cookie-secret", "cookie-secure", "csrf-disabled", "csrf-secure", "session-disabled", "debug", "profile-mismatch"} {
		t.Run(check, func(t *testing.T) {
			driver := &securityWarningDriver{}
			logger := log.NewLog(driver)
			t.Cleanup(func() { _ = logger.Close() })
			app := strictSecurityTestApp(t, "staging")
			app.log = logger
			setStrictSecurityValue(t, app, "app.security_strict", true)
			setStrictSecurityValue(t, app, "app.security_profile", string(SecurityProfileBrowserCookie))
			setStrictSecurityValue(t, app, "app.session_enable", true)
			setStrictSecurityValue(t, app, "app.csrf_enable", true)
			setStrictSecurityValue(t, app, "csrf.secure", true)
			setStrictSecurityValue(t, app, "app.server.allowed_hosts", []string{"staging.example.test"})
			cookieConfig := cookie.DefaultConfig()
			cookieConfig.Secret, cookieConfig.Secure = strings.Repeat("c", 32), true
			marker := ""
			switch check {
			case "valid-api":
				setStrictSecurityValue(t, app, "app.security_profile", string(SecurityProfileStatelessAPI))
				setStrictSecurityValue(t, app, "app.session_enable", false)
				setStrictSecurityValue(t, app, "app.csrf_enable", false)
				cookieConfig.Secret, cookieConfig.Secure = "", false
			case "missing-hosts":
				setStrictSecurityValue(t, app, "app.server.allowed_hosts", []string{})
				marker = "allowed_hosts"
			case "wildcard-hosts":
				setStrictSecurityValue(t, app, "app.server.allowed_hosts", []string{"staging.example.test", "*"})
				marker = "allowed_hosts"
			case "cookie-secret":
				cookieConfig.Secret, marker = "", "Cookie/Session 密钥"
			case "cookie-secure":
				cookieConfig.Secure, marker = false, "Cookie/Session Cookie 未启用 Secure"
			case "csrf-disabled":
				setStrictSecurityValue(t, app, "app.csrf_enable", false)
				marker = "CSRF 未启用"
			case "csrf-secure":
				setStrictSecurityValue(t, app, "csrf.secure", false)
				marker = "CSRF Cookie 未启用 Secure"
			case "session-disabled":
				setStrictSecurityValue(t, app, "app.session_enable", false)
				marker = "browser_cookie 必须启用 Session"
			case "debug":
				setStrictSecurityValue(t, app, "app.app_debug", true)
				marker = "app_debug"
			case "profile-mismatch":
				setStrictSecurityValue(t, app, "app.security_profile", string(SecurityProfileStatelessAPI))
				marker = "stateless_api"
			}
			factory, err := cookie.NewCookieWithConfig(cookieConfig)
			if err != nil {
				t.Fatal(err)
			}
			app.cookie = factory
			app.warnProductionSecurity()
			app.warnProductionSecurity()
			if err := app.StartupError(); marker == "" && err != nil || marker != "" && (err == nil || !strings.Contains(err.Error(), marker)) {
				t.Fatalf("check=%s, marker=%q, error=%v", check, marker, err)
			}
			if err := logger.Flush(stdcontext.Background()); err != nil {
				t.Fatal(err)
			}
			messages := driver.snapshot()
			seen := make(map[string]bool)
			for _, message := range messages {
				if !strings.HasPrefix(message, "严格模式安全警告") {
					t.Fatalf("misleading environment diagnostic: %s", message)
				}
				if seen[message] || strings.Contains(message, strings.Repeat("c", 32)) {
					t.Fatal("duplicated diagnostic or leaked secret")
				}
				seen[message] = true
			}
			if marker == "" && len(messages) != 0 || marker != "" && len(messages) == 0 {
				t.Fatalf("unexpected diagnostics: %v", messages)
			}
		})
	}
}

func TestStrictSecurityInitializationAndRunAdmission(t *testing.T) {
	for _, valid := range []bool{false, true} {
		base := t.TempDir()
		writeTestAppConfigFiles(t, base)
		hosts := []string{}
		if valid {
			hosts = []string{"staging.example.test"}
		}
		contents, err := json.Marshal(map[string]interface{}{
			"app_env": "staging", "app_debug": false, "security_strict": true,
			"security_profile": "stateless_api", "database_startup_policy": "disabled",
			"session_enable": false, "csrf_enable": false,
			"server": map[string]interface{}{"allowed_hosts": hosts},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "config", "app.json"), contents, 0o600); err != nil {
			t.Fatal(err)
		}
		app := NewAppUninitialized(base)
		t.Cleanup(func() { _ = app.Close() })
		err = app.Initialize()
		if valid {
			if err != nil || app.State() != ApplicationStateInitialized || app.Environment() != "staging" {
				t.Fatalf("valid staging configuration failed initialization: %v %v", app.State(), err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), "allowed_hosts") || app.State() != ApplicationStateFailed {
				t.Fatalf("unsafe staging configuration initialized: %v %v", app.State(), err)
			}
			lease, err := app.AcquireRunLease()
			if lease != nil || err == nil {
				t.Fatal("unsafe staging configuration entered running state")
			}
		}
	}
}
