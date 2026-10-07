package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

func TestIssue27CommandDeclaresLoopback(t *testing.T) {
	cmd := &Run{}
	cmd.Configure()
	found := false
	for _, option := range cmd.GetOptionDefinitions() {
		if option.Name == "host" {
			found = true
			if option.Default != "127.0.0.1" {
				t.Fatalf("default host=%q", option.Default)
			}
		}
	}
	if !found {
		t.Fatal("missing --host option")
	}
}

// TestIssue27HostReachesBothLaunchPaths 验证实际启动编排写入配置和传给子进程的环境，
// 不启动 air 或真实外网监听；不改写进程环境，避免测试之间互相污染。
func TestIssue27HostReachesBothLaunchPaths(t *testing.T) {
	for _, hotReload := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			args    []string
			want    string
			invalid bool
		}{
			{"default", nil, "127.0.0.1", false},
			{"explicit_ipv4", []string{"--host", "0.0.0.0"}, "0.0.0.0", false},
			{"unsupported_ipv6_unchanged", []string{"--host", "::"}, "", true},
			{"explicit_hostname", []string{"--host", "localhost"}, "localhost", false},
		} {
			mode := "normal/"
			if hotReload {
				mode = "air/"
			}
			t.Run(mode+tc.name, func(t *testing.T) {
				app := buildConsoleTestApp(t, t.TempDir())
				environment := map[string]string{applicationServerHostEnvironment: "192.0.2.1"}
				launches := 0
				check := func() {
					t.Helper()
					if got := environment[applicationServerHostEnvironment]; got != tc.want {
						t.Errorf("child host=%q want=%q", got, tc.want)
					}
					server, ok := app.Config().Get("app.server").(map[string]interface{})
					if !ok || server["host"] != tc.want {
						t.Errorf("server config=%#v", server)
					}
					launches++
				}
				ops := runOperations{
					lookupEnvironment: func(k string) (string, bool) { v, ok := environment[k]; return v, ok },
					setEnvironment:    func(k, v string) error { environment[k] = v; return nil },
					unsetEnvironment:  func(k string) error { delete(environment, k); return nil },
					lookupExecutable: func(string) (string, error) {
						if hotReload {
							return "air", nil
						}
						return "", errors.New("air absent")
					},
					newHTTPKernel: func(*framework.App) (framework.Kernel, error) {
						if hotReload {
							t.Fatal("air path constructed normal kernel")
						}
						return runTestKernel{}, nil
					},
					runApplication: func(*framework.App) error { check(); return nil },
					runAir: func(string, string) error {
						if !hotReload {
							t.Fatal("normal path launched air")
						}
						check()
						return nil
					},
				}
				output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
				cmd := &Run{Command: console.Command{App: app}}
				err := cmd.executeWithOperations(console.NewInput(tc.args...), output, ops)
				if tc.invalid {
					if err == nil || launches != 0 || len(environment) != 1 || environment[applicationServerHostEnvironment] != "192.0.2.1" {
						t.Fatalf("rejected address mutated launch state: error=%v launches=%d env=%v", err, launches, environment)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if launches != 1 {
					t.Fatalf("launches=%d", launches)
				}
			})
		}
	}
}

// TestIssue27GeneratedProjectConfig 检查实际写入新项目的配置；准备回调只隔离依赖下载。
func TestIssue27GeneratedProjectConfig(t *testing.T) {
	base := t.TempDir()
	if err := createProject(context.Background(), base, "demo", func(context.Context, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	readConfig := func(name string) map[string]interface{} {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(base, "demo", "config", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]interface{}
		if err = json.Unmarshal(content, &values); err != nil {
			t.Fatal(err)
		}
		return values
	}
	server, ok := readConfig("app")["server"].(map[string]interface{})
	if !ok || server["host"] != "127.0.0.1" || server["port"] != float64(8000) {
		t.Fatalf("scaffold server=%#v", server)
	}
	if readConfig("route")["url_route_must"] != true {
		t.Fatal("explicit route default was reverted")
	}
	if readConfig("cookie")["httponly"] != true {
		t.Fatal("cookie HttpOnly default was reverted")
	}
}

func TestIssue27DockerKeepsExplicitContainerBind(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = writeDockerDistribution(root, buildTarget{os: "linux", arch: "amd64"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Dockerfile":         "ENV APP_SERVER_HOST=0.0.0.0 APP_SERVER_PORT=8000",
		"docker-compose.yml": "APP_SERVER_HOST: \"0.0.0.0\"",
	} {
		b, err := root.ReadFile(name)
		if err != nil || !strings.Contains(string(b), want) {
			t.Fatalf("%s: missing container override: %v", name, err)
		}
	}
}

// TestIssue27ReferenceProjectConfig 避免参考项目继续示范非显式的全接口开发监听。
func TestIssue27ReferenceProjectConfig(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "project", "config", "app.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Server struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"server"`
	}
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatal(err)
	}
	if config.Server.Host != "127.0.0.1" || config.Server.Port != 8000 {
		t.Fatalf("reference server config: %#v", config.Server)
	}
}
