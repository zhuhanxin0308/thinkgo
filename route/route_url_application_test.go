package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

func TestRouterURLForRequestKeepsSingleApplicationPath(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	if err = registered.WithName("users.show"); err != nil {
		t.Fatalf("设置路由名称失败: %v", err)
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/18", nil))
	builtURL, err := router.URLForRequest(request, "users.show", map[string]interface{}{"id": 18, "tab": "profile"})
	if err != nil {
		t.Fatalf("生成请求级路由 URL 失败: %v", err)
	}
	if builtURL != "/users/18.html?tab=profile" {
		t.Fatalf("单应用路由 URL 错误，实际为 %q", builtURL)
	}
}

// TestRouterURLForRequestUsesVisibleApplicationPrefix 验证路径应用、映射别名
// 和域名绑定三种入口生成与当前请求一致的站内 URL。
func TestRouterURLForRequestUsesVisibleApplicationPrefix(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册多应用命名路由失败: %v", err)
	}
	if err := registered.WithName("users.show"); err != nil {
		t.Fatalf("设置多应用路由名称失败: %v", err)
	}
	tests := []struct {
		name        string
		application fwcontext.ApplicationContext
		expected    string
	}{
		{
			name:        "mapped path",
			application: fwcontext.NewApplicationContextWithURLPrefix("admin", "/backend/users/18", "/users/18", "/backend", "/backend", "example.com", "example.com", false),
			expected:    "/backend/users/18.html",
		},
		{
			name:        "domain binding",
			application: fwcontext.NewApplicationContextWithURLPrefix("admin", "/users/18", "/users/18", "", "", "admin.example.com", "admin.example.com", true),
			expected:    "/users/18.html",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/18", nil))
			request.SetApplicationContext(test.application)
			builtURL, err := router.URLForRequest(request, "users.show", map[string]interface{}{"id": 18})
			if err != nil || builtURL != test.expected {
				t.Fatalf("应用路由 URL 错误: url=%q err=%v", builtURL, err)
			}
		})
	}
}
