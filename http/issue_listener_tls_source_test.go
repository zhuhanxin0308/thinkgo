package http

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/deploy"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/httplistener"
)

// Configuration and diagnostics are compared without opening a listener. A
// readable certificate marker and absent key distinguish the selected paths
// before any certificate parsing or trust-store dependency.
func TestDeploymentTLSMatchesEffectiveHTTPListener(t *testing.T) {
	for _, applicationCount := range []int{0, 1, 2} {
		for _, source := range []string{"root", "root-disabled", "environment", "explicit-host-override"} {
			t.Run(fmt.Sprintf("applications-%d/%s", applicationCount, source), func(t *testing.T) {
				basePath := t.TempDir()
				rootEnabled := source != "root-disabled"
				writeNativeHTTPConfig(t, basePath, fmt.Sprintf(`{"app_env":"test","database_startup_policy":"disabled","server":{"tls":{"enable":%t,"cert_file":"root.pem","key_file":"root.key"}}}`, rootEnabled))
				for _, name := range []string{"index", "admin"} {
					writeIssue149HTTPConfig(t, filepath.Join(basePath, "app", name, "config", "app.json"), map[string]interface{}{
						"server": map[string]interface{}{"tls": map[string]interface{}{
							"enable": !rootEnabled, "cert_file": "child-" + name + ".pem", "key_file": "child-" + name + ".key",
						}},
					})
				}
				want := httplistener.TLSConfig{Enable: rootEnabled, CertFile: "root.pem", KeyFile: "root.key"}
				if source == "environment" {
					t.Setenv("APP_SERVER_TLS_ENABLE", "false")
					t.Setenv("APP_SERVER_TLS_CERT_FILE", "environment.pem")
					t.Setenv("APP_SERVER_TLS_KEY_FILE", "environment.key")
					want = httplistener.TLSConfig{CertFile: "environment.pem", KeyFile: "environment.key"}
				}
				project := framework.NewConsoleAppUninitialized(basePath)
				t.Cleanup(func() { _ = project.Close() })
				if applicationCount > 0 {
					loader := func(*framework.App) error { return nil }
					definitions := []framework.ApplicationDefinition{{Name: "index", Register: loader}}
					if applicationCount > 1 {
						definitions = append(definitions, framework.ApplicationDefinition{Name: "admin", Register: loader})
					}
					if err := project.RegisterApplications(loader, definitions...); err != nil {
						t.Fatal(err)
					}
				}
				applications, err := project.BuildApplications()
				if err != nil {
					t.Fatal(err)
				}
				if err := project.Initialize(); err != nil {
					t.Fatal(err)
				}
				if source == "explicit-host-override" {
					if err := project.ApplyProjectApplicationOverrides(map[string]interface{}{
						"server.tls.enable": true, "server.tls.cert_file": "override.pem", "server.tls.key_file": "override.key",
					}); err != nil {
						t.Fatal(err)
					}
					want = httplistener.TLSConfig{Enable: true, CertFile: "override.pem", KeyFile: "override.key"}
				}
				if applicationCount == 0 && source == "root" {
					// The ordinary host must follow its live service rather than an
					// earlier snapshot, unlike a compiled application's host.
					if err := project.Config().Set("app.server.tls.cert_file", "ordinary.pem"); err != nil {
						t.Fatal(err)
					}
					want.CertFile = "ordinary.pem"
				}
				want = want.AtPath(basePath)
				if err := os.WriteFile(want.CertFile, []byte("listener-material-marker"), 0o600); err != nil {
					t.Fatal(err)
				}
				kernel, err := NewHttp(project)
				if err != nil {
					t.Fatalf("project HTTP configuration: %v", err)
				}
				actual := httplistener.TLSConfig{Enable: kernel.srvConf.EnableTLS, CertFile: kernel.srvConf.CertFile, KeyFile: kernel.srvConf.KeyFile}
				if actual != want {
					t.Fatalf("project listener configuration = %#v, want %#v", actual, want)
				}
				for name, current := range applications {
					if err := current.Initialize(); err != nil {
						t.Fatalf("initialize %s: %v", name, err)
					}
					publicHost, err := NewHttp(current)
					if err != nil {
						t.Fatalf("public host for %s: %v", name, err)
					}
					publicTLS := httplistener.TLSConfig{Enable: publicHost.srvConf.EnableTLS, CertFile: publicHost.srvConf.CertFile, KeyFile: publicHost.srvConf.KeyFile}
					if publicTLS != actual {
						t.Fatalf("public host for %s escaped the project listener: %#v, want %#v", name, publicTLS, actual)
					}
					applicationHandler, err := newHttp(current, false)
					if err != nil {
						t.Fatalf("application handler for %s: %v", name, err)
					}
					serviceTLS, err := httplistener.ApplicationTLS(current.Config().Get("app"), current.BasePath)
					if err != nil {
						t.Fatal(err)
					}
					handlerTLS := httplistener.TLSConfig{Enable: applicationHandler.srvConf.EnableTLS, CertFile: applicationHandler.srvConf.CertFile, KeyFile: applicationHandler.srvConf.KeyFile}
					if handlerTLS != serviceTLS {
						t.Fatalf("internal handler for %s lost its application configuration: %#v, want %#v", name, handlerTLS, serviceTLS)
					}
					raw, err := httplistener.HostApplicationConfig(current)
					if err != nil {
						t.Fatal(err)
					}
					diagnostic, err := httplistener.ApplicationTLS(raw, current.BasePath)
					if err != nil || diagnostic != actual {
						t.Fatalf("%s diagnostic material diverged: %#v, %v", name, diagnostic, err)
					}
					wantMessage := "生产部署未启用 TLS"
					if want.Enable {
						wantMessage = "TLS 私钥文件不可用"
					}
					report := deploy.NewDefaultAuditor().Run(context.Background(), current)
					found := false
					for _, result := range report.Results {
						if result.Name == "http.tls" {
							found = true
							if result.Level != deploy.LevelFail || result.Message != wantMessage {
								t.Fatalf("%s deployment checked different listener material: %#v", name, result)
							}
						}
					}
					if !found {
						t.Fatal("default deployment report omitted TLS diagnostics")
					}
				}
			})
		}
	}
}

func writeIssue149HTTPConfig(t *testing.T, path string, values map[string]interface{}) {
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
