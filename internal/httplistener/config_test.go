package httplistener_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/httplistener"
)

func TestHostSourceUsesProjectSnapshotOnlyForCompiledApplications(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "ordinary"
		if native {
			name = "one-compiled-application"
		}
		t.Run(name, func(t *testing.T) {
			basePath := t.TempDir()
			writeListenerProject(t, basePath)
			app := framework.NewConsoleAppUninitialized(basePath)
			t.Cleanup(func() { _ = app.Close() })
			if native {
				if err := os.MkdirAll(filepath.Join(basePath, "app", "index", "config"), 0o700); err != nil {
					t.Fatal(err)
				}
				child := `{"server":{"tls":{"enable":false,"cert_file":"child.pem","key_file":"child.key"}}}`
				if err := os.WriteFile(filepath.Join(basePath, "app", "index", "config", "app.json"), []byte(child), 0o600); err != nil {
					t.Fatal(err)
				}
				loader := func(*framework.App) error { return nil }
				if err := app.RegisterApplications(loader, framework.ApplicationDefinition{Name: "index", Register: loader}); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.Initialize(); err != nil {
				t.Fatal(err)
			}
			if !native {
				// A post-initialization service override is effective for an ordinary
				// application, while its earlier project snapshot stays unchanged.
				if err := app.Config().Set("app.server.tls", map[string]interface{}{
					"enable": false, "cert_file": "child.pem", "key_file": "child.key",
				}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := httplistener.HostApplicationConfig(app)
			if err != nil {
				t.Fatal(err)
			}
			host, err := httplistener.ApplicationTLS(raw, basePath)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "child"
			if native {
				prefix = "root"
			}
			if host.Enable != native || host.CertFile != filepath.Join(basePath, prefix+".pem") || host.KeyFile != filepath.Join(basePath, prefix+".key") {
				t.Fatalf("host chose the wrong configuration source: %#v", host)
			}
			raw, err = httplistener.ApplicationConfig(app, false)
			if err != nil {
				t.Fatal(err)
			}
			application, err := httplistener.ApplicationTLS(raw, basePath)
			if err != nil || application.Enable || application.CertFile != filepath.Join(basePath, "child.pem") {
				t.Fatalf("application handler lost its own configuration: %#v, %v", application, err)
			}
		})
	}
}

func TestListenerTLSDefaultsAndPaths(t *testing.T) {
	basePath := t.TempDir()
	defaults, err := httplistener.ApplicationTLS(nil, basePath)
	if err != nil || defaults.Enable || defaults.CertFile != filepath.Join(basePath, "runtime", "cert.pem") || defaults.KeyFile != filepath.Join(basePath, "runtime", "key.pem") {
		t.Fatalf("missing TLS settings must preserve listener defaults: %#v, %v", defaults, err)
	}
	absoluteKey := filepath.Join(t.TempDir(), "absolute.key")
	configured, err := httplistener.ApplicationTLS(map[string]interface{}{
		"server": map[string]interface{}{"tls": map[string]interface{}{
			"enable": true, "cert_file": "  ./certs/../server.pem  ", "key_file": "  " + absoluteKey + "  ",
		}},
	}, basePath)
	if err != nil || !configured.Enable || configured.CertFile != filepath.Join(basePath, "server.pem") || configured.KeyFile != absoluteKey {
		t.Fatalf("relative and absolute material paths diverged: %#v, %v", configured, err)
	}
}

func TestNativeChildWithoutCatalogUsesProjectListener(t *testing.T) {
	basePath := t.TempDir()
	writeListenerProject(t, basePath)
	for _, name := range []string{"index", "admin"} {
		if err := os.MkdirAll(filepath.Join(basePath, "app", name, "config"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(basePath, "app", "admin", "config", "app.json"), []byte(`{"server":{"tls":{"enable":false,"cert_file":"admin.pem","key_file":"admin.key"}}}`), 0o600); err != nil {
		t.Fatal(err)
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
	admin := applications["admin"]
	if err := admin.Initialize(); err != nil {
		t.Fatal(err)
	}
	if len(admin.ApplicationNames()) != 0 || admin.CurrentApplicationName() != "admin" {
		t.Fatal("fixture must use a real child that does not own the project catalog")
	}
	raw, err := httplistener.HostApplicationConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	host, err := httplistener.ApplicationTLS(raw, basePath)
	if err != nil || !host.Enable || host.CertFile != filepath.Join(basePath, "root.pem") || host.KeyFile != filepath.Join(basePath, "root.key") {
		t.Fatalf("selected native child used its own TLS override: %#v, %v", host, err)
	}
}

func TestListenerTLSRejectsInvalidConfigurationShapes(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  interface{}
	}{
		{name: "application", raw: true},
		{name: "server", raw: map[string]interface{}{"server": "invalid"}},
		{name: "tls", raw: tlsApplication("invalid")},
		{name: "enable-string", raw: tlsApplication(map[string]interface{}{"enable": "true"})},
		{name: "enable-number", raw: tlsApplication(map[string]interface{}{"enable": 1})},
		{name: "certificate-type", raw: tlsApplication(map[string]interface{}{"cert_file": false})},
		{name: "key-type", raw: tlsApplication(map[string]interface{}{"key_file": []string{"key.pem"}})},
		{name: "unknown-option", raw: tlsApplication(map[string]interface{}{"insecure_skip_verify": true})},
		{name: "empty-certificate", raw: tlsApplication(map[string]interface{}{"enable": true, "cert_file": " "})},
		{name: "empty-key", raw: tlsApplication(map[string]interface{}{"enable": true, "key_file": " "})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := httplistener.ApplicationTLS(test.raw, t.TempDir()); err == nil {
				t.Fatal("invalid listener configuration was accepted")
			}
		})
	}
}

func TestListenerSourceRejectsUnavailableApplication(t *testing.T) {
	if _, err := httplistener.HostApplicationConfig(nil); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("nil application must retain its public error: %v", err)
	}
	app := framework.NewConsoleAppUninitialized(t.TempDir())
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := httplistener.ApplicationConfig(app, false); !errors.Is(err, framework.ErrApplicationClosed) {
		t.Fatalf("closed configuration service must be unavailable: %v", err)
	}
}

func tlsApplication(tls interface{}) map[string]interface{} {
	return map[string]interface{}{"server": map[string]interface{}{"tls": tls}}
}

func writeListenerProject(t *testing.T, basePath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(basePath, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"app.json":     `{"app_env":"test","server":{"tls":{"enable":true,"cert_file":"root.pem","key_file":"root.key"}}}`,
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
}
