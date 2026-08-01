package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/db"
	"thinkgo/framework/route"
)

// TestMultiHttpRunsWithoutDatabase 验证每个应用都缺少数据库配置时，
// 多应用管理器仍能启动、处理真实 HTTP 请求并按逆序关闭应用。
func TestMultiHttpRunsWithoutDatabase(t *testing.T) {
	basePath := t.TempDir()
	writeMultiHTTPNoDatabaseFixture(t, basePath)

	events := make([]string, 0, 4)
	adminProvider := &multiHTTPNoDatabaseProvider{name: "admin", events: &events}
	indexProvider := &multiHTTPNoDatabaseProvider{name: "index", events: &events}
	definitions := []framework.ApplicationDefinition{
		newMultiHTTPNoDatabaseDefinition("index", indexProvider),
		newMultiHTTPNoDatabaseDefinition("admin", adminProvider),
	}
	manager, err := framework.NewApplicationManagerFromDefinitions(basePath, definitions, false)
	if err != nil {
		t.Fatalf("创建无数据库多应用管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	for name, app := range manager.Applications() {
		if startupErr := app.StartupError(); startupErr != nil {
			t.Fatalf("应用 %q 缺少数据库配置时不应产生启动错误: %v", name, startupErr)
		}
		managerService, resolveErr := framework.ResolveServiceAs[*db.Manager](app, framework.ServiceDBManager)
		if resolveErr != nil {
			t.Fatalf("应用 %q 数据库管理器不可用: %v", name, resolveErr)
		}
		if managerService == nil {
			t.Fatalf("应用 %q 数据库管理器不应为空", name)
		}
		if app.Has(string(framework.ServiceDB)) {
			database, dbErr := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
			if dbErr != nil || database != nil {
				t.Fatalf("应用 %q 不应建立默认数据库连接，db=%v err=%v", name, database, dbErr)
			}
		}
	}
	if err = manager.Boot(); err != nil {
		t.Fatalf("无数据库多应用启动失败: %v", err)
	}

	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建无数据库统一 HTTP 宿主失败: %v", err)
	}
	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.com/admin/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("无数据库 HTTP 请求状态错误，实际为 %d", recorder.Code)
	}
	if body := recorder.Body.String(); body != "admin|database-free" {
		t.Fatalf("无数据库 HTTP 请求响应错误，实际为 %q", body)
	}

	if err = manager.Close(); err != nil {
		t.Fatalf("关闭无数据库多应用失败: %v", err)
	}
	wantEvents := []string{"admin:boot", "index:boot", "index:shutdown", "admin:shutdown"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("无数据库多应用生命周期顺序错误，want=%#v got=%#v", wantEvents, events)
	}
}

type multiHTTPNoDatabaseProvider struct {
	name   string
	events *[]string
}

func (provider *multiHTTPNoDatabaseProvider) Register(app *framework.App) error {
	return app.RegisterRouteLoader(func(app *framework.App) error {
		router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
		if err != nil {
			return err
		}
		_, err = router.Any("/health", func(request *fwcontext.Request) *fwcontext.Response {
			application, _ := request.ApplicationContext()
			return fwcontext.NewResponse().Content(application.Name() + "|database-free")
		})
		return err
	})
}

func (provider *multiHTTPNoDatabaseProvider) Boot(_ *framework.App) error {
	*provider.events = append(*provider.events, provider.name+":boot")
	return nil
}

func (provider *multiHTTPNoDatabaseProvider) Shutdown(_ *framework.App) error {
	*provider.events = append(*provider.events, provider.name+":shutdown")
	return nil
}

func newMultiHTTPNoDatabaseDefinition(name string, provider *multiHTTPNoDatabaseProvider) framework.ApplicationDefinition {
	return framework.ApplicationDefinition{
		Name: name,
		Path: filepath.ToSlash(filepath.Join("app", name)),
		Register: func(app *framework.App) error {
			return app.RegisterProvider(provider)
		},
	}
}

func writeMultiHTTPNoDatabaseFixture(t *testing.T, basePath string) {
	t.Helper()
	applicationNames := []string{"index", "admin"}
	directories := []string{filepath.Join(basePath, "config")}
	for _, name := range applicationNames {
		directories = append(directories,
			filepath.Join(basePath, "app", name, "config"),
			filepath.Join(basePath, "app", name, "lang"),
		)
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("创建无数据库多应用测试目录失败: %v", err)
		}
	}

	configs := map[string]interface{}{
		"app.json": map[string]interface{}{
			"app_env": "test", "app_debug": false, "app_trace": false, "default_app": "index", "app_express": false,
			"session_enable": false,
			"server":         map[string]interface{}{"host": "127.0.0.1", "port": 18080},
			"compression":    map[string]interface{}{"enable": false},
		},
		"cache.json": map[string]interface{}{
			"default": "file",
			"stores":  map[string]interface{}{"file": map[string]interface{}{"type": "file", "path": "runtime/cache"}},
		},
		"cookie.json":  map[string]interface{}{"path": "/", "secure": false, "httponly": true, "samesite": "Lax"},
		"csrf.json":    map[string]interface{}{"safe_methods": []string{"GET", "HEAD", "OPTIONS"}},
		"lang.json":    map[string]interface{}{"default_lang": "zh-cn"},
		"log.json":     map[string]interface{}{"default": "file", "channels": map[string]interface{}{"file": map[string]interface{}{"type": "file", "path": "runtime/log"}}},
		"route.json":   map[string]interface{}{"url_route_must": true},
		"session.json": map[string]interface{}{"name": "TESTSESSID", "type": "file", "storage_path": "runtime/session", "cookie_path": "/", "expire": 1440},
		"view.json":    map[string]interface{}{"view_path": "app/view", "view_suffix": "html", "cache": false},
	}
	for name, value := range configs {
		writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", name), value)
	}

	databaseConfig := map[string]interface{}{
		"default": "default",
		"connections": map[string]interface{}{
			"default": map[string]interface{}{"type": "mysql", "database": "unused"},
		},
	}
	databasePaths := []string{filepath.Join(basePath, "config", "database.json")}
	for _, name := range applicationNames {
		databasePaths = append(databasePaths, filepath.Join(basePath, "app", name, "config", "database.json"))
	}
	for _, path := range databasePaths {
		writeMultiHTTPTestJSON(t, path, databaseConfig)
		if err := os.Remove(path); err != nil {
			t.Fatalf("删除应用数据库配置 %s 失败: %v", path, err)
		}
	}
}
