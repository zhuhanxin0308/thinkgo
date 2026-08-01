package route

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/context"
	frameworkRoute "thinkgo/framework/route"
)

// TestLoadRegistersNamedRoutes 验证示例应用路由加载器注册完整且稳定的命名路由。
func TestLoadRegistersNamedRoutes(t *testing.T) {
	app := mustBuildTestApp(t)
	t.Cleanup(func() { _ = app.Close() })

	if err := Load(app); err != nil {
		t.Fatalf("加载示例路由失败: %v", err)
	}
	router := mustTestRouter(t, app)
	routes, err := router.Routes()
	if err != nil {
		t.Fatalf("读取示例路由失败: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("示例路由数量错误: got %d, routes=%#v", len(routes), routes)
	}
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		seen[route.Name] = true
	}
	if !seen["home"] || !seen["users.index"] {
		t.Fatalf("示例命名路由缺失: %#v", seen)
	}
}

// TestLoadRejectsDuplicateHomeRoute 验证已存在首页路由时不会静默覆盖。
func TestLoadRejectsDuplicateHomeRoute(t *testing.T) {
	app := mustBuildTestApp(t)
	t.Cleanup(func() { _ = app.Close() })
	router := mustTestRouter(t, app)
	if _, err := router.Get("/", func(*context.Request) *context.Response {
		return context.NewResponse().Code(http.StatusNoContent)
	}); err != nil {
		t.Fatalf("预注册首页路由失败: %v", err)
	}
	if err := Load(app); err == nil {
		t.Fatal("重复首页路由应返回错误")
	}
}

// TestLoadRejectsDuplicateHomeName 验证首页名称冲突会在命名阶段返回错误。
func TestLoadRejectsDuplicateHomeName(t *testing.T) {
	app := mustBuildTestApp(t)
	t.Cleanup(func() { _ = app.Close() })
	router := mustTestRouter(t, app)
	r, err := router.Get("/other", func(*context.Request) *context.Response {
		return context.NewResponse()
	})
	if err != nil {
		t.Fatalf("预注册冲突路由失败: %v", err)
	}
	if err := r.WithName("home"); err != nil {
		t.Fatalf("预注册冲突名称失败: %v", err)
	}
	if err := Load(app); err == nil {
		t.Fatal("重复首页名称应返回错误")
	}
}

// TestLoadRejectsDuplicateUsersName 验证用户资源名称冲突不会被覆盖。
func TestLoadRejectsDuplicateUsersName(t *testing.T) {
	app := mustBuildTestApp(t)
	t.Cleanup(func() { _ = app.Close() })
	router := mustTestRouter(t, app)
	r, err := router.Get("/other", func(*context.Request) *context.Response {
		return context.NewResponse()
	})
	if err != nil {
		t.Fatalf("预注册冲突路由失败: %v", err)
	}
	if err := r.WithName("users.index"); err != nil {
		t.Fatalf("预注册冲突名称失败: %v", err)
	}
	if err := Load(app); err == nil {
		t.Fatal("重复用户名称应返回错误")
	}
}

// TestLoadRejectsDuplicateUsersRoute 验证用户资源路由冲突不会被静默覆盖。
func TestLoadRejectsDuplicateUsersRoute(t *testing.T) {
	app := mustBuildTestApp(t)
	t.Cleanup(func() { _ = app.Close() })
	router := mustTestRouter(t, app)
	if _, err := router.Get("/api/users", "User@Other"); err != nil {
		t.Fatalf("预注册用户资源路由失败: %v", err)
	}
	if err := Load(app); err == nil {
		t.Fatal("重复用户资源路由应返回错误")
	}
}

// TestLoadRejectsUnavailableApplication 验证路由加载器在应用依赖不可用时快速失败。
func TestLoadRejectsUnavailableApplication(t *testing.T) {
	if err := Load(nil); err == nil {
		t.Fatal("空应用不应允许加载路由")
	}
}

func mustBuildTestApp(t *testing.T) *framework.App {
	t.Helper()
	basePath := t.TempDir()
	writeRouteTestConfig(t, basePath)
	app, err := framework.BuildApp(basePath)
	if err != nil {
		t.Fatalf("构建测试应用失败: %v", err)
	}
	return app
}

// writeRouteTestConfig 为路由单元测试准备严格构造所需的基础配置。
func writeRouteTestConfig(t *testing.T, basePath string) {
	t.Helper()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建路由测试配置目录失败: %v", err)
	}
	configs := map[string]string{
		"app.json":     `{"app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`,
		"log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cache.json":   `{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`,
		"view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"cookie.json":  `{}`,
		"session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入路由测试配置 %q 失败: %v", name, err)
		}
	}
}

// mustTestRouter 通过公开服务解析边界取得测试路由器。
func mustTestRouter(t *testing.T, app *framework.App) *frameworkRoute.Router {
	t.Helper()
	router, err := framework.ResolveServiceAs[*frameworkRoute.Router](app, framework.ServiceRoute)
	if err != nil {
		t.Fatalf("解析测试路由器失败: %v", err)
	}
	return router
}
