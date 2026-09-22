// Package testkit 使用真实应用生命周期构建隔离 HTTP 测试宿主，不监听网络端口。
package testkit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	fwhttp "github.com/zhuhanxin0308/thinkgo/framework/http"
)

const (
	testServerPort      = 8080
	testSecretBytes     = 32
	testDirectoryMode   = 0o700
	testFileMode        = 0o600
	testDatabaseDefault = "default"
)

var configSectionPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Options 定义测试配置与装配时序。Config 使用 app.server.max_body_bytes 等配置路径。
// Register 在初始化前注册业务组件；Configure 在初始化后、Provider 启动前替换测试依赖。
// Database 加入应用默认连接管理器，关闭宿主时一并关闭；每个宿主应使用独立连接。
type Options struct {
	Config    map[string]any
	Register  func(*framework.App) error
	Configure func(*framework.App) error
	Database  *db.DB
}

// Host 拥有独立的临时目录、应用容器与 HTTP 内核，可并发处理测试请求。
type Host struct {
	app     *framework.App
	handler http.Handler
}

// New 构造完整测试宿主，启动失败立即报告测试错误，并自动注册资源清理。
func New(t testing.TB, options Options) *Host {
	t.Helper()
	host, err := newHost(t.TempDir(), options)
	if err != nil {
		t.Fatalf("创建 ThinkGo 测试宿主失败: %v", err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Errorf("关闭 ThinkGo 测试宿主失败: %v", err)
		}
	})
	return host
}

func newHost(basePath string, options Options) (_ *Host, returnErr error) {
	if err := writeTestConfig(basePath, options.Config); err != nil {
		return nil, err
	}
	app := framework.NewConsoleAppUninitialized(basePath)
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, app.Close())
		}
	}()
	if options.Register != nil {
		if err := options.Register(app); err != nil {
			return nil, fmt.Errorf("注册测试应用: %w", err)
		}
	}
	if err := app.Initialize(); err != nil {
		return nil, err
	}
	if options.Database != nil {
		manager, err := framework.ResolveServiceAs[*db.Manager](app, framework.ServiceDBManager)
		if err != nil {
			return nil, err
		}
		name := app.Config().GetString("database.default", testDatabaseDefault)
		if err := manager.Add(name, options.Database); err != nil {
			return nil, err
		}
		if err := app.Instance(string(framework.ServiceDB), options.Database); err != nil {
			return nil, err
		}
	}
	if options.Configure != nil {
		if err := options.Configure(app); err != nil {
			return nil, fmt.Errorf("装配测试依赖: %w", err)
		}
	}
	if err := app.EnsureReady(); err != nil {
		return nil, err
	}
	if err := app.LoadRoutes(); err != nil {
		return nil, err
	}
	if _, err := app.Route().Routes(); err != nil {
		return nil, err
	}
	handler, err := fwhttp.NewHttp(app)
	if err != nil {
		return nil, err
	}
	return &Host{app: app, handler: handler}, nil
}

// App 返回测试应用，可检查容器、配置和数据库状态；运行期仍遵守框架注册冻结规则。
func (host *Host) App() *framework.App { return host.app }

// ServeHTTP 通过真实 HTTP 内核执行路由、中间件、绑定、注入及请求作用域清理。
func (host *Host) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	host.handler.ServeHTTP(writer, request)
}

// Close 关闭宿主拥有的服务；可与 testing.Cleanup 重复调用。
func (host *Host) Close() error {
	if host == nil || host.app == nil {
		return nil
	}
	return host.app.Close()
}

// writeTestConfig 只在测试临时目录创建配置，每个宿主使用独立随机 Cookie 密钥。
func writeTestConfig(basePath string, overrides map[string]any) error {
	secret := make([]byte, testSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	defaults := map[string]any{
		"app": map[string]any{
			"app_env": "test", "app_debug": false, "session_enable": false,
			"server":      map[string]any{"host": "127.0.0.1", "port": testServerPort, "allowed_hosts": []string{"example.com", "localhost", "127.0.0.1"}},
			"compression": map[string]any{"enable": false},
		},
		"cache":   map[string]any{"default": "memory", "stores": map[string]any{"memory": map[string]any{"type": "memory"}}},
		"cookie":  map[string]any{"secret": hex.EncodeToString(secret)},
		"session": map[string]any{"type": "memory", "name": "TESTSESSID", "expire": 0},
		"log":     map[string]any{"close": true, "default": "file", "channels": map[string]any{"file": map[string]any{"type": "file", "path": "runtime/log"}}},
		"route":   map[string]any{"url_route_must": true},
		"view":    map[string]any{"view_path": "app/view", "view_suffix": "html", "cache": false},
	}
	configuration := config.NewConfig()
	for key, value := range defaults {
		if err := configuration.Set(key, value); err != nil {
			return err
		}
	}
	// 父级配置先应用，子级路径随后覆盖，避免 map 迭代顺序改变最终设置。
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := configuration.Set(key, overrides[key]); err != nil {
			return err
		}
	}
	for name, value := range configuration.GetMap("") {
		if !configSectionPattern.MatchString(name) {
			return fmt.Errorf("测试配置段名称非法: %q", name)
		}
		content, err := json.Marshal(value)
		if err != nil {
			return err
		}
		directory := filepath.Join(basePath, "config")
		if err := os.MkdirAll(directory, testDirectoryMode); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, name+".json"), content, testFileMode); err != nil {
			return err
		}
	}
	return nil
}
