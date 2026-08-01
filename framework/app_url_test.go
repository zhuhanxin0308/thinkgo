package framework

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"thinkgo/framework/config"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/route"
)

func TestAppURLForUsesRequestApplicationScopeWithoutSharedMutation(t *testing.T) {
	app := &App{ApplicationName: "admin"}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/admin/users", nil))
	request.SetApplicationContext(fwcontext.NewApplicationContext("admin", "/admin/users", "/users", "/admin", "example.com", false))

	if got := app.URLFor(request, "/users?tag=go"); got != "https://localhost/admin/users?tag=go" {
		t.Fatalf("路径应用 URL 错误，实际为 %q", got)
	}
	if app.ApplicationName != "admin" {
		t.Fatal("生成 URL 不能修改 App 的当前应用状态")
	}

	domainRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://admin.example.com/users", nil))
	domainRequest.SetApplicationContext(fwcontext.NewApplicationContext("admin", "/users", "/users", "", "admin.example.com", true))
	if got := app.URLFor(domainRequest, "/users"); got != "https://admin.example.com/users" {
		t.Fatalf("域名绑定应用 URL 错误，实际为 %q", got)
	}
}

// TestAppURLForUsesCanonicalDomainHost 验证域名应用生成 URL 时不采信伪造的请求 Host。
func TestAppURLForUsesCanonicalDomainHost(t *testing.T) {
	app := &App{ApplicationName: "admin"}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://attacker.example.com/users", nil))
	request.SetApplicationContext(fwcontext.NewApplicationContextWithCanonicalHost(
		"admin", "/users", "/users", "", "attacker.example.com", "admin.example.com", true,
	))

	if got := app.URLFor(request, "/reset"); got != "https://admin.example.com/reset" {
		t.Fatalf("域名应用 URL 必须使用可信 Host，实际为 %q", got)
	}
}

func TestAppRouteURLUsesApplicationScopedRouterURL(t *testing.T) {
	app := &App{ApplicationName: "admin", route: route.NewRouter()}
	registered, err := app.route.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册测试路由失败: %v", err)
	}
	if err = registered.WithName("users.show"); err != nil {
		t.Fatalf("设置测试路由名称失败: %v", err)
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/admin/users/18", nil))
	request.SetApplicationContext(fwcontext.NewApplicationContext("admin", "/admin/users/18", "/users/18", "/admin", "example.com", false))
	builtURL, err := app.RouteURL(request, "users.show", map[string]interface{}{"id": 18, "keyword": "go"})
	if err != nil {
		t.Fatalf("生成应用路由 URL 失败: %v", err)
	}
	if builtURL != "https://localhost/admin/users/18?keyword=go" {
		t.Fatalf("应用路由 URL 错误，实际为 %q", builtURL)
	}
}

func TestAppURLForApplicationUsesMappingAndDomainBinding(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]interface{}
		wantURL  string
	}{
		{
			name: "app_map 路径别名",
			settings: map[string]interface{}{
				"app_map": map[string]interface{}{"dashboard": "admin"},
			},
			wantURL: "https://localhost/dashboard/users",
		},
		{
			name: "精确域名绑定",
			settings: map[string]interface{}{
				"domain_bind": map[string]interface{}{"admin.example.com": "admin"},
			},
			wantURL: "http://admin.example.com/users",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, index, _ := newApplicationURLTestManager(test.settings)
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
			request.SetApplicationContext(fwcontext.NewApplicationContext("index", "/", "/", "", "example.com", false))

			got, err := index.URLForApplication(request, "admin", "/users")
			if err != nil {
				t.Fatalf("生成目标应用 URL 失败: %v", err)
			}
			if got != test.wantURL {
				t.Fatalf("目标应用 URL 错误，实际为 %q，期望为 %q", got, test.wantURL)
			}
			if manager.applicationResolver == nil {
				t.Fatal("应用管理器应该持有经过校验的应用解析器")
			}
		})
	}
}

func TestAppRouteURLForApplicationUsesTargetRouter(t *testing.T) {
	_, index, admin := newApplicationURLTestManager(map[string]interface{}{
		"app_map": map[string]interface{}{"dashboard": "admin"},
	})
	registered, err := admin.route.Get("/users/:id", "AdminUser@Show")
	if err != nil {
		t.Fatalf("注册目标应用路由失败: %v", err)
	}
	if err = registered.WithName("users.show"); err != nil {
		t.Fatalf("设置目标应用路由名称失败: %v", err)
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	request.SetApplicationContext(fwcontext.NewApplicationContext("index", "/", "/", "", "example.com", false))
	builtURL, err := index.RouteURLForApplication(request, "admin", "users.show", map[string]interface{}{"id": 18})
	if err != nil {
		t.Fatalf("生成目标应用路由 URL 失败: %v", err)
	}
	if builtURL != "https://localhost/dashboard/users/18" {
		t.Fatalf("目标应用路由 URL 错误，实际为 %q", builtURL)
	}
}

func newApplicationURLTestManager(settings map[string]interface{}) (*ApplicationManager, *App, *App) {
	configuration := config.NewConfig()
	for key, value := range settings {
		configuration.Set("app."+key, value)
	}
	index := &App{ApplicationName: "index", config: configuration, route: route.NewRouter()}
	admin := &App{ApplicationName: "admin", config: configuration, route: route.NewRouter()}
	manager := &ApplicationManager{
		applicationNames: []string{"index", "admin"},
		applications: map[string]*App{
			"index": index,
			"admin": admin,
		},
		defaultAppName: "index",
	}
	index.applicationManager = manager
	admin.applicationManager = manager
	resolver, err := NewApplicationResolver(manager)
	if err != nil {
		panic(err)
	}
	manager.applicationResolver = resolver
	return manager, index, admin
}
