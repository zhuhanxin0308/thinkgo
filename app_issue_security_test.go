package framework

import (
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
)

func TestIssue19ProductionDebugFailsClosed(t *testing.T) {
	for _, environment := range []string{"production", "prod", "release"} {
		t.Run(environment, func(t *testing.T) {
			app := &App{config: config.NewConfig(), DebugMode: true}
			for key, value := range map[string]interface{}{
				"app.app_env": environment, "app.security_profile": string(SecurityProfileStatelessAPI),
				"app.session_enable": false, "app.csrf_enable": false,
				"app.server.allowed_hosts": []string{"example.com"},
			} {
				if err := app.config.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			app.warnProductionSecurity()
			if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "debug") {
				t.Fatalf("production debug accepted: %v", err)
			}
		})
	}
	app := &App{config: config.NewConfig(), DebugMode: true}
	if err := app.config.Set("app.app_env", "development"); err != nil {
		t.Fatal(err)
	}
	app.warnProductionSecurity()
	if err := app.StartupError(); err != nil {
		t.Fatal(err)
	}
}

func TestIssue21EnabledCorsRequiresExplicitOrigins(t *testing.T) {
	if _, _, err := createAppCors(map[string]interface{}{"enable": true}); err == nil {
		t.Fatal("CORS enabled without explicit origins")
	}
	for _, origins := range [][]string{{"https://client.example"}, {"*"}} {
		if _, enabled, err := createAppCors(map[string]interface{}{"enable": true, "allow_origins": origins}); err != nil || !enabled {
			t.Fatalf("explicit origins rejected: %v", err)
		}
	}
	if _, enabled, err := createAppCors(nil); err != nil || enabled {
		t.Fatalf("disabled defaults changed: %v", err)
	}
}

func TestIssue47MySQLDSNRedaction(t *testing.T) {
	for _, message := range []string{
		"connect alice:dsn-secret@tcp(db.internal:3306)/app failed",
		"connect alice:dsn-secret@unix(/var/run/mysql.sock)/app failed",
		"alice:p@ss/word:dsn-secret@tcp(localhost)/app",
		"alice:line\ndsn-secret@tcp6([::1])/app",
	} {
		result := redactDatabaseConnectionError(errors.New(message))
		if strings.Contains(result, "dsn-secret") || !strings.Contains(result, "[REDACTED]") {
			t.Fatalf("DSN credential escaped redaction: %s", result)
		}
	}
	ordinary := "dial tcp 127.0.0.1:3306: connection refused"
	if got := redactDatabaseConnectionError(errors.New(ordinary)); got != ordinary {
		t.Fatalf("diagnostic corrupted: %q", got)
	}
}

func TestIssue19ChecksRuntimeAndConfigurationBeforeProfile(t *testing.T) {
	for _, tc := range []struct {
		name                string
		runtime, configured bool
		profile             string
	}{
		{"runtime", true, false, string(SecurityProfileStatelessAPI)},
		{"configuration", false, true, string(SecurityProfileStatelessAPI)},
		{"invalid_profile", false, true, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{config: config.NewConfig(), DebugMode: tc.runtime}
			for key, value := range map[string]interface{}{
				"app.app_env": "production", "app.app_debug": tc.configured,
				"app.security_profile": tc.profile, "app.session_enable": false,
				"app.csrf_enable": false, "app.server.allowed_hosts": []string{"example.com"},
			} {
				if err := app.config.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			app.warnProductionSecurity()
			if err := app.StartupError(); err == nil || !strings.Contains(err.Error(), "app_debug") {
				t.Fatalf("debug not blocked: %v", err)
			}
		})
	}
}

func TestIssue21CorsStillRejectsInvalidExplicitPolicies(t *testing.T) {
	for _, values := range []map[string]interface{}{
		{"enable": true, "allow_origins": []string{}},
		{"enable": true, "allow_origins": nil},
		{"enable": true, "allow_origins": []string{""}},
		{"enable": true, "allow_origins": []string{"*"}, "allow_credentials": true},
		{"enable": true, "allow_origins": []interface{}{1}},
	} {
		if _, _, err := createAppCors(values); err == nil {
			t.Fatalf("invalid explicit CORS policy accepted: %#v", values)
		}
	}
}
