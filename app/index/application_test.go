package index

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"thinkgo/app/index/controller"
	"thinkgo/framework"
	fwhttp "thinkgo/framework/http"
	"thinkgo/framework/route"
)

// TestRegisterRejectsNilApplication 验证应用注册入口不会接受空应用。
func TestRegisterRejectsNilApplication(t *testing.T) {
	if err := Register(nil); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用应返回 ErrNilApplication: %v", err)
	}
}

// TestRegisterRejectsDuplicateController 验证控制器名称冲突会在注册阶段暴露。
func TestRegisterRejectsDuplicateController(t *testing.T) {
	basePath := t.TempDir()
	app := framework.NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterController("User", &controller.User{}); err != nil {
		t.Fatalf("预注册控制器失败: %v", err)
	}
	if err := Register(app); err == nil {
		t.Fatal("重复控制器注册应返回错误")
	}
}

func TestIndexApplicationDefinitionLoadsItsComponents(t *testing.T) {
	definitions := framework.ApplicationDefinitions()
	if len(definitions) != 1 || definitions[0].Name != "index" {
		t.Fatalf("应该只注册 index 应用，实际为 %#v", definitions)
	}
	definition := definitions[0]
	if definition.Path != "app/index" || definition.Register == nil {
		t.Fatalf("index 应用定义不完整: %#v", definition)
	}

	basePath := t.TempDir()
	writeIndexTestConfig(t, basePath)
	manager, err := framework.NewApplicationManagerFromDefinitions(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建 index 应用管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	host, err := fwhttp.NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://example.com/api/users", nil)
	request.Header.Set("X-Request-ID", "test-request-id")
	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "User List" {
		application, _ := manager.Application("index")
		router, resolveErr := framework.ResolveServiceAs[*route.Router](application, framework.ServiceRoute)
		if resolveErr != nil {
			t.Fatalf("index 应用路由服务不可用: %v", resolveErr)
		}
		routes, routeErr := router.Routes()
		t.Fatalf("index 应用路由没有调用 User 控制器: status=%d body=%q startup=%v routes=%#v routeErr=%v", recorder.Code, recorder.Body.String(), application.StartupError(), routes, routeErr)
	}
	if recorder.Header().Get("X-Request-ID") != "test-request-id" {
		t.Fatalf("index 应用中间件没有传递请求 ID: %q", recorder.Header().Get("X-Request-ID"))
	}
}

// writeIndexTestConfig 为严格构造入口提供最小且完整的测试配置。
func writeIndexTestConfig(t *testing.T, basePath string) {
	t.Helper()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建 index 测试配置目录失败: %v", err)
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
			t.Fatalf("写入 index 测试配置 %q 失败: %v", name, err)
		}
	}
}
