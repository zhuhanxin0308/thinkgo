package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
)

func TestNativeTLSUsesHostMaterialAndBothTargetScopes(t *testing.T) {
	for _, test := range []struct {
		name     string
		dnsNames []string
		wantPass bool
	}{
		{name: "both-scopes", dnsNames: []string{"host.example.test", "index.example.test", "admin.example.test"}, wantPass: true},
		{name: "missing-host", dnsNames: []string{"index.example.test", "admin.example.test"}},
		{name: "missing-applications", dnsNames: []string{"host.example.test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
			pki := newTLSTestPKI(t, now, tlsTestLeafOptions{dnsNames: test.dnsNames})
			project, applications := newIssue149DeployProject(t)
			writeTLSTestMaterial(t, project.BasePath, pki.leafKey, 0o600, pki.leafDER, pki.intermediateDER)
			for name, current := range applications {
				if err := current.Initialize(); err != nil {
					t.Fatalf("initialize %s: %v", name, err)
				}
				// The child switches TLS off and names nonexistent material. The
				// process listener must still validate the project's actual pair.
				if current.Config().GetBool("app.server.tls.enable") {
					t.Fatal("fixture did not retain the child TLS override")
				}
				result := checkTLSFilesWithOptions(current, tlsValidationOptions{now: now, roots: pki.rootPool})
				if test.wantPass {
					want := LevelPass
					if runtime.GOOS == "windows" {
						want = LevelWarn
					}
					if result.Level != want || !strings.Contains(result.Message, "2 个服务目标") {
						t.Fatalf("%s did not validate the host pair and deduplicated target union: %#v", name, result)
					}
				} else if result.Level != LevelFail || result.Message != "TLS 证书 SAN 未覆盖全部配置目标" {
					t.Fatalf("%s omitted a host/application target: %#v", name, result)
				}
				if result := checkProductionConfiguration(context.Background(), current); result.Level != LevelFail || !strings.Contains(result.Message, `app_env="test"`) {
					t.Fatalf("unrelated application checks must retain ServiceConfig: %#v", result)
				}
			}
		})
	}
}

func TestTLSTargetUnionAllowsEmptySourceButRejectsInvalidDeclarations(t *testing.T) {
	empty := config.NewConfig()
	host := config.NewConfig()
	if err := host.Set("app.server", map[string]interface{}{
		"domain": "https://HOST.example.test", "allowed_hosts": []string{"host.example.test:443"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, configurations := range [][]*config.Config{{empty, host}, {host, empty}, {host, host}} {
		targets, err := configuredTLSTargets(configurations...)
		if err != nil || !reflect.DeepEqual(targets, []string{"host.example.test"}) {
			t.Fatalf("valid union with an empty/duplicate source failed: %#v, %v", targets, err)
		}
	}
	_, err := configuredTLSTargets(empty, empty)
	assertTLSValidationCode(t, err, TLSValidationTarget)
	_, err = configuredTLSTargets(host, nil)
	assertTLSValidationCode(t, err, TLSValidationTarget)
	for _, values := range []map[string]interface{}{
		{"domain": "http://insecure.example.test"},
		{"domain": 42},
		{"allowed_hosts": "app.example.test"},
		{"allowed_hosts": []string{""}},
		{"allowed_hosts": []string{"*"}},
	} {
		invalid := config.NewConfig()
		if err := invalid.Set("app.server", values); err != nil {
			t.Fatal(err)
		}
		for _, configurations := range [][]*config.Config{{host, invalid}, {invalid, host}} {
			_, err := configuredTLSTargets(configurations...)
			assertTLSValidationCode(t, err, TLSValidationTarget)
		}
	}
}

func TestTLSMaterialValidationRetainsExplicitConfiguration(t *testing.T) {
	now := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	pki := newTLSTestPKI(t, now, tlsTestLeafOptions{dnsNames: []string{"explicit.example.test"}})
	app, live := newDeployTestApp(t)
	cert, key := writeTLSTestMaterial(t, app.BasePath, pki.leafKey, 0o600, pki.leafDER, pki.intermediateDER)
	explicit := config.NewConfig()
	if err := explicit.Set("app.server", map[string]interface{}{
		"tls":           map[string]interface{}{"enable": true, "cert_file": cert, "key_file": key},
		"allowed_hosts": []string{"explicit.example.test"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := live.Set("app.server.tls", map[string]interface{}{"enable": false, "cert_file": "unused.pem", "key_file": "unused.key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTLSMaterial(app, explicit, tlsValidationOptions{now: now, roots: pki.rootPool}); err != nil {
		t.Fatalf("explicit material validation unexpectedly selected the live listener: %v", err)
	}
}

func newIssue149DeployProject(t *testing.T) (*framework.App, map[string]*framework.App) {
	t.Helper()
	basePath := t.TempDir()
	writeIssue149DeployJSON(t, filepath.Join(basePath, "config", "app.json"), map[string]interface{}{
		"app_env":                 "production",
		"database_startup_policy": "disabled",
		"server": map[string]interface{}{
			"domain":        "https://host.example.test",
			"allowed_hosts": []string{"host.example.test"},
			"tls":           map[string]interface{}{"enable": true, "cert_file": "server-cert.pem", "key_file": "server-key.pem"},
		},
	})
	for name, content := range map[string]string{
		"log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cache.json":   `{"default":"memory","stores":{"memory":{"type":"memory"}}}`,
		"view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"cookie.json":  `{}`,
		"session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
	} {
		if err := os.WriteFile(filepath.Join(basePath, "config", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"index", "admin"} {
		writeIssue149DeployJSON(t, filepath.Join(basePath, "app", name, "config", "app.json"), map[string]interface{}{
			"app_env": "test",
			"server": map[string]interface{}{
				"domain":        "https://" + name + ".example.test",
				"allowed_hosts": []string{name + ".example.test"},
				"tls":           map[string]interface{}{"enable": false, "cert_file": "unused-" + name + ".pem", "key_file": "unused-" + name + ".key"},
			},
		})
	}
	project := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	loader := func(*framework.App) error { return nil }
	if err := project.RegisterApplications(loader,
		framework.ApplicationDefinition{Name: "index", Register: loader},
		framework.ApplicationDefinition{Name: "admin", Register: loader},
	); err != nil {
		t.Fatal(err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatal(err)
	}
	return project, applications
}

func writeIssue149DeployJSON(t *testing.T, path string, values map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
