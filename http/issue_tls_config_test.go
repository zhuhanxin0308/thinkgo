package http

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestTLSConfigPathsUseApplicationRoot(t *testing.T) {
	launcher := t.TempDir()
	t.Chdir(launcher)
	projectCert, projectKey, _ := protocolTestCertificate(t)
	launcherCert, launcherKey, _ := protocolTestCertificate(t)
	copyTLSConfigFixture(t, launcherCert, filepath.Join(launcher, "runtime", "cert.pem"))
	copyTLSConfigFixture(t, launcherKey, filepath.Join(launcher, "runtime", "key.pem"))
	wantPair, err := tls.LoadX509KeyPair(projectCert, projectKey)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"relative", "default", "absolute"} {
		t.Run(mode, func(t *testing.T) {
			project := t.TempDir()
			ensureHTTPTestConfigFiles(t, project)
			certPath := filepath.Join(project, "runtime", "cert.pem")
			keyPath := filepath.Join(project, "runtime", "key.pem")
			copyTLSConfigFixture(t, projectCert, certPath)
			copyTLSConfigFixture(t, projectKey, keyPath)
			tlsValues := map[string]interface{}{"enable": true}
			switch mode {
			case "relative":
				tlsValues["cert_file"] = "./runtime/cert.pem"
				tlsValues["key_file"] = "./runtime/key.pem"
			case "absolute":
				tlsValues["cert_file"] = certPath
				tlsValues["key_file"] = keyPath
			}
			content, err := json.Marshal(map[string]interface{}{
				"app_env": "test",
				"server":  map[string]interface{}{"tls": tlsValues},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(project, "config", "app.json"), content, 0o600); err != nil {
				t.Fatal(err)
			}
			app, err := framework.BuildConsoleApp(project)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.Close() })
			for name, parse := range map[string]func(*framework.App) (serverConf, compressionConf, error){
				"application": parseHTTPConfig,
				"project":     parseProjectHTTPConfig,
			} {
				server, _, err := parse(app)
				if err != nil {
					t.Fatalf("%s config: %v", name, err)
				}
				if server.CertFile != certPath || server.KeyFile != keyPath {
					t.Fatalf("%s used paths %q / %q, want %q / %q", name, server.CertFile, server.KeyFile, certPath, keyPath)
				}
				pair, err := tls.LoadX509KeyPair(server.CertFile, server.KeyFile)
				if err != nil {
					t.Fatalf("%s listener material: %v", name, err)
				}
				if !bytes.Equal(pair.Certificate[0], wantPair.Certificate[0]) {
					t.Fatalf("%s loaded the launcher's certificate instead of the project's", name)
				}
			}
		})
	}
}

func copyTLSConfigFixture(t *testing.T, source, destination string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAllowedHostWildcardMatchesCertificateLabelDepth(t *testing.T) {
	handler := &Http{srvConf: serverConf{AllowedHosts: []string{"*.tenant.example.test"}}}
	certificate := &x509.Certificate{DNSNames: []string{"*.tenant.example.test"}}
	for _, test := range []struct {
		host string
		want bool
	}{
		{host: "one.tenant.example.test", want: true},
		{host: "ONE.TENANT.EXAMPLE.TEST.:443", want: true},
		{host: "two.one.tenant.example.test"},
		{host: "tenant.example.test"},
		{host: "not-tenant.example.test"},
	} {
		t.Run(test.host, func(t *testing.T) {
			if got := handler.isAllowedHost(test.host); got != test.want {
				t.Fatalf("HTTP wildcard accepted = %v, want %v", got, test.want)
			}
			if covered := certificate.VerifyHostname(normalizeHTTPHost(test.host)) == nil; covered != test.want {
				t.Fatalf("certificate wildcard covered = %v, want %v", covered, test.want)
			}
		})
	}
	handler.srvConf.AllowedHosts = append(handler.srvConf.AllowedHosts, "two.one.tenant.example.test")
	if !handler.isAllowedHost("two.one.tenant.example.test:443") {
		t.Fatal("an explicit deeper hostname must remain allowed")
	}
}

func TestHTTPEntrypointsEnforceSingleLabelHostWildcard(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "ServeHTTP"
		if direct {
			name = "Run"
		}
		t.Run(name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			mustHTTPConfig(t, app).Set("app.server.allowed_hosts", []interface{}{"*.tenant.example.test", "explicit.one.tenant.example.test"})
			calls := 0
			mustHTTPRoute(t, app).Get("/wildcard", func(*fwcontext.Request) *fwcontext.Response {
				calls++
				return fwcontext.NewResponse().Content("allowed")
			})
			handler := newTestHTTPHandler(t, app)
			wantCalls := 0
			for _, test := range []struct {
				host   string
				status int
			}{
				{host: "one.tenant.example.test", status: stdhttp.StatusOK},
				{host: "two.one.tenant.example.test", status: stdhttp.StatusMisdirectedRequest},
				{host: "tenant.example.test", status: stdhttp.StatusMisdirectedRequest},
				{host: "not-tenant.example.test", status: stdhttp.StatusMisdirectedRequest},
				{host: "explicit.one.tenant.example.test", status: stdhttp.StatusOK},
			} {
				raw := httptest.NewRequest(stdhttp.MethodGet, "http://"+test.host+"/wildcard", nil)
				recorder := httptest.NewRecorder()
				var status int
				if direct {
					request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(recorder))
					response := handler.Run(request)
					status = response.GetStatus()
					handler.End(response)
				} else {
					handler.ServeHTTP(recorder, raw)
					status = recorder.Code
				}
				if status != test.status {
					t.Fatalf("host %q status = %d, want %d", test.host, status, test.status)
				}
				if test.status == stdhttp.StatusOK {
					wantCalls++
				}
				if calls != wantCalls {
					t.Fatalf("host %q: handler calls = %d, want %d", test.host, calls, wantCalls)
				}
			}
		})
	}
}
