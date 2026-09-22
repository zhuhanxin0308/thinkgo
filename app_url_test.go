package framework

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

func TestAppURLForUsesSingleApplicationDomain(t *testing.T) {
	app := &App{}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://attacker.example.com/users", nil))

	if got := app.URLFor(request, "/users?tag=go"); got != "https://localhost/users?tag=go" {
		t.Fatalf("单应用 URL 不应追加应用前缀或采信请求 Host，实际为 %q", got)
	}
	if got := app.URLFor(request, "https://static.example.com/app.js"); got != "https://static.example.com/app.js" {
		t.Fatalf("完整 HTTP URL 不应被改写，实际为 %q", got)
	}
}

func TestAppRouteURLUsesSingleApplicationRouterURL(t *testing.T) {
	app := &App{route: route.NewRouter()}
	registered, err := app.route.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册测试路由失败: %v", err)
	}
	if err = registered.WithName("users.show"); err != nil {
		t.Fatalf("设置测试路由名称失败: %v", err)
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/18", nil))
	builtURL, err := app.RouteURL(request, "users.show", map[string]interface{}{"id": 18, "keyword": "go"})
	if err != nil {
		t.Fatalf("生成单应用路由 URL 失败: %v", err)
	}
	if builtURL != "https://localhost/users/18.html?keyword=go" {
		t.Fatalf("单应用路由 URL 错误，实际为 %q", builtURL)
	}
}

// TestAppURLAPIsUseCurrentApplicationPrefix 验证 App.URLFor、AssetURLFor 和
// RouteURL 都沿用当前请求的可见应用映射前缀，同时仍使用项目可信域名。
func TestAppURLAPIsUseCurrentApplicationPrefix(t *testing.T) {
	app := &App{route: route.NewRouter()}
	registered, err := app.route.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册多应用测试路由失败: %v", err)
	}
	if err := registered.WithName("users.show"); err != nil {
		t.Fatalf("设置多应用测试路由名称失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://attacker.example.com/backend/users", nil))
	request.SetApplicationContext(fwcontext.NewApplicationContextWithURLPrefix(
		"admin", "/backend/users", "/users", "/backend", "/backend",
		"attacker.example.com", "attacker.example.com", false,
	))

	if got := app.URLFor(request, "/dashboard"); got != "https://localhost/backend/dashboard" {
		t.Fatalf("URLFor 未使用应用前缀: %q", got)
	}
	if got := app.AssetURLFor(request, "assets/app.css"); got != "https://localhost/backend/assets/app.css" {
		t.Fatalf("AssetURLFor 未使用应用前缀: %q", got)
	}
	builtURL, err := app.RouteURL(request, "users.show", map[string]interface{}{"id": 18})
	if err != nil || builtURL != "https://localhost/backend/users/18.html" {
		t.Fatalf("RouteURL 应用前缀错误: url=%q err=%v", builtURL, err)
	}
}
