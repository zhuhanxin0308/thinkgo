package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestStrictSecurityHTTPAdmissionAndHostBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		hosts  []string
		strict interface{}
	}{
		{"valid", []string{"staging.example.test"}, true},
		{"empty-hosts", []string{}, true},
		{"wildcard", []string{"*"}, true},
		{"invalid-switch", []string{"staging.example.test"}, "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			ensureHTTPTestConfigFiles(t, base)
			contents, err := json.Marshal(map[string]interface{}{
				"app_env": "staging", "app_debug": false, "security_strict": test.strict,
				"security_profile": "stateless_api", "database_startup_policy": "disabled",
				"session_enable": false, "csrf_enable": false,
				"server": map[string]interface{}{"allowed_hosts": test.hosts},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(base, "config", "app.json"), contents, 0o600); err != nil {
				t.Fatal(err)
			}
			app := framework.NewAppUninitialized(base)
			t.Cleanup(func() { _ = app.Close() })
			calls := 0
			app.Route().Get("/strict-security", func(*fwcontext.Request) *fwcontext.Response {
				calls++
				return fwcontext.NewResponse().Data("allowed")
			})
			handler, err := NewHttp(app)
			if err != nil {
				t.Fatal(err)
			}
			for _, host := range []string{"staging.example.test", "untrusted.example.test"} {
				writer := httptest.NewRecorder()
				request := httptest.NewRequest(stdhttp.MethodGet, "http://"+host+"/strict-security", nil)
				handler.ServeHTTP(writer, request)
				want := stdhttp.StatusInternalServerError
				if test.name == "valid" {
					want = stdhttp.StatusMisdirectedRequest
					if host == "staging.example.test" {
						want = stdhttp.StatusOK
					}
				}
				if writer.Code != want {
					t.Fatalf("host=%s status=%d want=%d body=%s", host, writer.Code, want, writer.Body.String())
				}
				if test.name != "valid" && (strings.Contains(writer.Body.String(), "allowed_hosts") || strings.Contains(writer.Body.String(), "security_strict")) {
					t.Fatal("startup configuration leaked into public response")
				}
			}
			wantCalls := 0
			if test.name == "valid" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("business handler calls=%d want=%d", calls, wantCalls)
			}
			if test.name != "valid" && (app.StartupError() == nil || app.State() != framework.ApplicationStateFailed) {
				t.Fatal("invalid application did not stay failed")
			}
		})
	}
}
