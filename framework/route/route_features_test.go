package route

import (
	"net/http/httptest"
	"reflect"
	"testing"

	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

func TestRouterOptionalParamAndNamedURL(t *testing.T) {
	router := NewRouter()
	router.Get("/users/:id/:tab?", "User@Show").Name("user.show")

	reqWithoutOptional := fwcontext.NewRequest(httptest.NewRequest("GET", "http://example.com/users/18", nil))
	matchedRoute, params := router.Match(reqWithoutOptional)
	if matchedRoute == nil || params["id"] != "18" || params["tab"] != "" {
		t.Fatalf("可选参数省略时匹配不正确，实际路由=%#v 参数=%#v", matchedRoute, params)
	}

	reqWithOptional := fwcontext.NewRequest(httptest.NewRequest("GET", "http://example.com/users/18/profile", nil))
	matchedRoute, params = router.Match(reqWithOptional)
	if matchedRoute == nil || params["tab"] != "profile" {
		t.Fatalf("可选参数存在时匹配不正确，实际参数=%#v", params)
	}

	url, err := router.URL("user.show", map[string]interface{}{"id": 18, "tab": "profile", "keyword": "go"})
	if err != nil {
		t.Fatalf("命名路由 URL 生成失败，错误为 %v", err)
	}
	if url != "/users/18/profile?keyword=go" {
		t.Fatalf("命名路由 URL 不正确，实际为 %q", url)
	}

	url, err = router.URL("user.show", map[string]interface{}{"id": 18})
	if err != nil {
		t.Fatalf("省略可选参数时 URL 生成失败，错误为 %v", err)
	}
	if url != "/users/18" {
		t.Fatalf("省略可选参数时 URL 不正确，实际为 %q", url)
	}
}

func TestRouterWithoutMiddlewareAndDomainGroup(t *testing.T) {
	router := NewRouter()
	groupMiddleware := middleware.Handler(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return next(req)
	})
	routeMiddleware := middleware.Handler(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return next(req)
	})

	router.DomainGroup("api.example.com", "/api", func() {
		router.Get("/users", "User@Index", routeMiddleware).WithoutMiddleware(groupMiddleware)
	}, groupMiddleware)

	req := fwcontext.NewRequest(httptest.NewRequest("GET", "http://api.example.com/api/users", nil))
	matchedRoute, _ := router.Match(req)
	if matchedRoute == nil {
		t.Fatal("DomainGroup 注册的路由应可匹配")
	}
	if len(matchedRoute.Middleware) != 1 {
		t.Fatalf("WithoutMiddleware 处理后中间件数量不正确，实际为 %d", len(matchedRoute.Middleware))
	}
	if reflect.ValueOf(matchedRoute.Middleware[0]).Pointer() != reflect.ValueOf(routeMiddleware).Pointer() {
		t.Fatalf("WithoutMiddleware 应移除组中间件，实际为 %#v", matchedRoute.Middleware)
	}
}

func TestResourceOnlyAndExcept(t *testing.T) {
	router := NewRouter()
	router.Resource("/posts", "Post").Only("index", "read")
	router.Resource("/comments", "Comment").Except("edit")

	if route, _ := router.Match(fwcontext.NewRequest(httptest.NewRequest("GET", "http://example.com/posts", nil))); route == nil {
		t.Fatal("Only 保留的资源路由应存在")
	}
	if route, _ := router.Match(fwcontext.NewRequest(httptest.NewRequest("POST", "http://example.com/posts", nil))); route != nil {
		t.Fatal("Only 未保留的资源路由不应存在")
	}
	if route, _ := router.Match(fwcontext.NewRequest(httptest.NewRequest("GET", "http://example.com/comments/9/edit", nil))); route != nil {
		t.Fatal("Except 排除的资源路由不应存在")
	}
	if route, _ := router.Match(fwcontext.NewRequest(httptest.NewRequest("DELETE", "http://example.com/comments/9", nil))); route == nil {
		t.Fatal("Except 未排除的资源路由应存在")
	}
}
