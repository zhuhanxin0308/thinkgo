package framework

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	frameworkcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestCreateAppCorsStrictConfiguration 验证应用层 CORS 配置严格解析并在禁用时仍完成校验。
func TestCreateAppCorsStrictConfiguration(t *testing.T) {
	handler, enabled, err := createAppCors(map[string]interface{}{
		"enable":            false,
		"allow_origins":     []interface{}{"https://client.example"},
		"allow_methods":     []interface{}{http.MethodGet, http.MethodPost},
		"allow_headers":     []interface{}{"Content-Type"},
		"expose_headers":    []interface{}{"X-Trace-ID"},
		"allow_credentials": true,
		"max_age":           600,
	})
	if err != nil || enabled || handler == nil {
		t.Fatalf("合法禁用配置仍应构造可复用别名: enabled=%t handler=%v err=%v", enabled, handler, err)
	}
	for _, values := range []map[string]interface{}{
		{"enable": false, "unknown": true},
		{"enable": false, "allow_origins": "https://client.example", "allow_methods": []interface{}{http.MethodGet}},
		{"enable": false, "allow_origins": []interface{}{"https://client.example"}, "allow_methods": []interface{}{http.MethodGet, 7}},
		{"enable": false, "allow_origins": []interface{}{"*"}, "allow_methods": []interface{}{http.MethodGet}, "allow_credentials": true},
	} {
		if _, _, err := createAppCors(values); err == nil {
			t.Fatalf("非法 CORS 配置必须在启动期被拒绝: %#v", values)
		}
	}
}

// TestInitializeInstallsConfiguredCors 验证环境覆盖后的 CORS 在业务中间件前完成预检。
func TestInitializeInstallsConfiguredCors(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	corsConfig := `{
  "enable": false,
  "allow_origins": ["https://default.example"],
  "allow_methods": ["GET", "POST"],
  "allow_headers": ["Content-Type"],
  "expose_headers": ["X-Trace-ID"],
  "allow_credentials": true,
  "max_age": 600
}`
	if err := os.WriteFile(filepath.Join(basePath, "config", "cors.json"), []byte(corsConfig), 0o600); err != nil {
		t.Fatalf("写入 CORS 测试配置失败: %v", err)
	}
	t.Setenv("CORS_ENABLE", "true")
	t.Setenv("CORS_ALLOW_ORIGINS", `["https://client.example"]`)

	app := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	businessCalled := false
	if err := app.RegisterGlobalMiddleware(func(*frameworkcontext.Request, func(*frameworkcontext.Request) *frameworkcontext.Response) *frameworkcontext.Response {
		businessCalled = true
		return frameworkcontext.NewResponse().Code(http.StatusUnauthorized)
	}); err != nil {
		t.Fatalf("注册测试业务中间件失败: %v", err)
	}
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化 CORS 测试应用失败: %v", err)
	}
	if app.middleware.ResolveAlias("cors") == nil {
		t.Fatal("应用初始化后必须注册 cors 别名")
	}
	raw := httptest.NewRequest(http.MethodOptions, "http://example.com/resource", nil)
	raw.Header.Set("Origin", "https://client.example")
	raw.Header.Set("Access-Control-Request-Method", http.MethodPost)
	raw.Header.Set("Access-Control-Request-Headers", "Content-Type")
	request := frameworkcontext.MustNewRequest(raw)
	called := false
	response := app.middleware.Then(request, func(*frameworkcontext.Request) *frameworkcontext.Response {
		called = true
		return frameworkcontext.NewResponse().Code(http.StatusOK)
	})
	if businessCalled || called || response.GetStatus() != http.StatusNoContent {
		t.Fatalf("启用的全局 CORS 应在业务中间件前直接完成预检: business=%t endpoint=%t response=%#v", businessCalled, called, response)
	}
	if response.Headers().Get("Access-Control-Allow-Origin") != "https://client.example" {
		t.Fatalf("CORS 环境来源覆盖未生效: %#v", response.Headers())
	}
}

// TestInitializeRejectsInvalidCorsConfiguration 验证非法 CORS 配置会阻止应用启动。
func TestInitializeRejectsInvalidCorsConfiguration(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	invalid := `{
  "enable": true,
  "allow_origins": ["*"],
  "allow_methods": ["GET"],
  "allow_credentials": true
}`
	if err := os.WriteFile(filepath.Join(basePath, "config", "cors.json"), []byte(invalid), 0o600); err != nil {
		t.Fatalf("写入非法 CORS 配置失败: %v", err)
	}
	app, err := initializeTestConsoleApp(t, basePath)
	if err == nil || app.StartupError() == nil || !strings.Contains(app.StartupError().Error(), "初始化 CORS 失败") {
		t.Fatalf("非法 CORS 配置必须形成稳定启动错误: initialize=%v startup=%v", err, app.StartupError())
	}
}
