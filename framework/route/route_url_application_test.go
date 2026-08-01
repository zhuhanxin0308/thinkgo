package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "thinkgo/framework/context"
)

func TestRouterURLForRequestAddsApplicationPathPrefix(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	if err = registered.WithName("users.show"); err != nil {
		t.Fatalf("设置路由名称失败: %v", err)
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/admin/users/18", nil))
	request.SetApplicationContext(fwcontext.NewApplicationContext("admin", "/admin/users/18", "/users/18", "/admin", "example.com", false))
	builtURL, err := router.URLForRequest(request, "users.show", map[string]interface{}{"id": 18, "tab": "profile"})
	if err != nil {
		t.Fatalf("生成请求级路由 URL 失败: %v", err)
	}
	if builtURL != "/admin/users/18?tab=profile" {
		t.Fatalf("请求级路由 URL 错误，实际为 %q", builtURL)
	}
}
