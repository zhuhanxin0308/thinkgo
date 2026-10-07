package http

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	frameworkconfig "github.com/zhuhanxin0308/thinkgo/v3/config"
)

func TestIssue27HTTPBindDefaultsAndOverrides(t *testing.T) {
	if framework.DefaultServerHost != "127.0.0.1" {
		t.Errorf("shared host default=%q", framework.DefaultServerHost)
	}
	for _, test := range []struct {
		name    string
		config  interface{}
		address string
	}{
		{"missing_app", nil, "127.0.0.1:8000"},
		{"empty_app", map[string]interface{}{}, "127.0.0.1:8000"},
		{"missing_server", map[string]interface{}{"app_env": "test"}, "127.0.0.1:8000"},
		{"null_server", map[string]interface{}{"server": nil}, "127.0.0.1:8000"},
		{"empty_server", map[string]interface{}{"server": map[string]interface{}{}}, "127.0.0.1:8000"},
		{"port_only", map[string]interface{}{"server": map[string]interface{}{"port": 8089}}, "127.0.0.1:8089"},
		{"explicit_external", map[string]interface{}{"server": map[string]interface{}{"host": "0.0.0.0"}}, "0.0.0.0:8000"},
		{"explicit_hostname", map[string]interface{}{"server": map[string]interface{}{"host": "localhost"}}, "localhost:8000"},
		{"explicit_ipv6", map[string]interface{}{"server": map[string]interface{}{"host": "::1"}}, "[::1]:8000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration, _, err := parseHTTPApplicationConfig(test.config)
			if err != nil {
				t.Fatal(err)
			}
			server := newConfiguredHTTPServer(nil, configuration, nil)
			if server.Addr != test.address {
				t.Errorf("address=%q want=%q", server.Addr, test.address)
			}
		})
	}
	for name, host := range map[string]interface{}{"empty": "", "spaces": " ", "null": nil, "number": 1} {
		t.Run("invalid_"+name, func(t *testing.T) {
			_, _, err := parseHTTPApplicationConfig(map[string]interface{}{"server": map[string]interface{}{"host": host}})
			if !errors.Is(err, ErrInvalidHTTPConfig) {
				t.Fatalf("invalid explicit host must not use default: %v", err)
			}
		})
	}
}

// TestIssue27DirectHTTPKernelAddress 覆盖直接构建应用的真实初始化与服务器装配，
// 不只测试解析器；不监听固定端口，不把配置断言当作实际网络隔离验证。
func TestIssue27DirectHTTPKernelAddress(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		mode := "initialized/"
		if lazy {
			mode = "lazy/"
		}
		for _, test := range []struct {
			name    string
			server  map[string]interface{}
			address string
		}{
			{"missing_server", nil, "127.0.0.1:8000"},
			{"empty_server", map[string]interface{}{}, "127.0.0.1:8000"},
			{"port_only", map[string]interface{}{"port": 8089}, "127.0.0.1:8089"},
			{"explicit_external", map[string]interface{}{"host": "0.0.0.0", "port": 8089}, "0.0.0.0:8089"},
		} {
			t.Run(mode+test.name, func(t *testing.T) {
				base := t.TempDir()
				ensureHTTPTestConfigFiles(t, base)
				values := map[string]interface{}{"app_env": "test"}
				if test.server != nil {
					values["server"] = test.server
				}
				content, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(base, "config", "app.json"), content, 0o600); err != nil {
					t.Fatal(err)
				}
				var app *framework.App
				if lazy {
					app = framework.NewConsoleAppUninitialized(base)
				} else {
					app, err = framework.BuildConsoleApp(base)
					if err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = app.Close() })
				kernel, err := NewHttp(app)
				if err != nil {
					t.Fatal(err)
				}
				if err := kernel.ensureInitialized(); err != nil {
					t.Fatal(err)
				}
				if got := kernel.newServer().Addr; got != test.address {
					t.Errorf("direct kernel address=%q want=%q", got, test.address)
				}
				project, _, err := parseProjectHTTPConfig(app)
				if err != nil {
					t.Fatal(err)
				}
				if got := newConfiguredHTTPServer(kernel, project, nil).Addr; got != test.address {
					t.Errorf("project snapshot address=%q want=%q", got, test.address)
				}
			})
		}
	}
}

func TestIssue27HTTPEnvironmentOverrideRemainsExplicit(t *testing.T) {
	configuration := frameworkconfig.NewConfig()
	configuration.Set("app", map[string]interface{}{"server": map[string]interface{}{"host": "127.0.0.1", "port": 8000}})
	if err := configuration.ApplyEnvironment(httpConfigEnvironment{"APP_SERVER_HOST": "0.0.0.0", "APP_SERVER_PORT": "8089"}); err != nil {
		t.Fatal(err)
	}
	server, _, err := parseHTTPApplicationConfig(configuration.Get("app"))
	if err != nil {
		t.Fatal(err)
	}
	if got := newConfiguredHTTPServer(nil, server, nil).Addr; got != "0.0.0.0:8089" {
		t.Fatalf("explicit environment address=%q", got)
	}
}
