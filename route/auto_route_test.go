package route

import (
	"errors"
	"net/http"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestAutoRouteEnabledByDefault 验证 ThinkPHP 默认 url_route_must=false。
func TestAutoRouteEnabledByDefault(t *testing.T) {
	router := NewRouter()
	req := context.MustNewRequest(newTestHTTPRequest("GET", "/user/index"))

	route, _ := matchForTest(t, router, req)
	if route == nil || route.Handler() != "user/index" {
		t.Fatalf("自动路由默认应保留 user/index 调度，实际为 %#v", route)
	}
}

// TestAutoRouteResolvesControllerAction 验证自动路由将 /user/edit 保留为 user/edit 调度。
func TestAutoRouteResolvesControllerAction(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.MustNewRequest(newTestHTTPRequest("GET", "/user/edit"))
	route, _ := matchForTest(t, router, req)

	if route == nil {
		t.Fatal("自动路由应匹配 /user/edit")
	}
	if handler, ok := route.Handler().(string); !ok || handler != "user/edit" {
		t.Fatalf("自动路由应解析为 user/edit，实际为 %v", route.Handler())
	}
}

// TestAutoRouteDefaultAction 验证 /user 映射到默认动作。
func TestAutoRouteDefaultAction(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.MustNewRequest(newTestHTTPRequest("GET", "/user"))
	route, _ := matchForTest(t, router, req)

	if route == nil {
		t.Fatal("自动路由应匹配 /user")
	}
	if handler, ok := route.Handler().(string); !ok || handler != "user/index" {
		t.Fatalf("自动路由应解析为 user/index，实际为 %v", route.Handler())
	}
}

// TestAutoRouteRootPath 验证根路径映射到默认控制器默认动作。
func TestAutoRouteRootPath(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.MustNewRequest(newTestHTTPRequest("GET", "/"))
	// 注意：根路径如果有注册路由会先匹配注册路由
	route, _ := matchForTest(t, router, req)

	if route == nil {
		t.Fatal("自动路由应匹配 /")
	}
	if handler, ok := route.Handler().(string); !ok || handler != "Index/index" {
		t.Fatalf("自动路由应解析为 Index/index，实际为 %v", route.Handler())
	}
}

// TestAutoRouteMultiLevel 验证多层级路径 /admin/user/edit 保留原始层级和动作名称。
func TestAutoRouteMultiLevel(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.MustNewRequest(newTestHTTPRequest("GET", "/admin/user/edit"))
	route, _ := matchForTest(t, router, req)

	if route == nil {
		t.Fatal("自动路由应匹配 /admin/user/edit")
	}
	if handler, ok := route.Handler().(string); !ok || handler != "admin/user/edit" {
		t.Fatalf("自动路由应解析为 admin/user/edit，实际为 %v", route.Handler())
	}
}

// TestAutoRouteExplicitRoutePriority 验证显式路由优先于自动路由。
func TestAutoRouteExplicitRoutePriority(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)
	router.Get("/user/edit", "CustomController@CustomAction")

	req := context.MustNewRequest(newTestHTTPRequest("GET", "/user/edit"))
	route, _ := matchForTest(t, router, req)

	if route == nil {
		t.Fatal("应匹配显式路由")
	}
	if handler, ok := route.Handler().(string); !ok || handler != "CustomController@CustomAction" {
		t.Fatalf("显式路由应优先于自动路由，实际为 %v", route.Handler())
	}
}

// TestAutoRouteCustomDefault 验证自定义默认控制器和动作。
func TestAutoRouteCustomDefault(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)
	router.SetDefaultController("Home")
	router.SetDefaultAction("main")

	req := context.MustNewRequest(newTestHTTPRequest("GET", "/"))
	route, _ := matchForTest(t, router, req)

	if route == nil {
		t.Fatal("自动路由应匹配 /")
	}
	if handler, ok := route.Handler().(string); !ok || handler != "Home/main" {
		t.Fatalf("自定义默认应解析为 Home/main，实际为 %v", route.Handler())
	}
}

// TestAutoRouteRejectsUnsafeSegments 验证自动路由只接受可映射到 Go 标识符的安全片段，
// 避免畸形 URL 被解析成容器名或方法名后进入控制器分发阶段。
func TestAutoRouteRejectsUnsafeSegments(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(true); err != nil {
		t.Fatalf("启用自动路由失败: %v", err)
	}

	malformedPaths := []string{
		"/../secret",
		"/admin//edit",
	}
	for _, path := range malformedPaths {
		req := context.MustNewRequest(newTestHTTPRequest(http.MethodGet, path))
		matched, _, err := router.Match(req)
		if matched != nil || !errors.Is(err, ErrInvalidRequestPath) {
			t.Fatalf("畸形路径 %q 应返回 ErrInvalidRequestPath，路由=%#v 错误=%v", path, matched, err)
		}
	}

	unsafeSegments := []string{
		"/user/show.json",
		"/user/list-all",
		"/123/index",
	}

	for _, path := range unsafeSegments {
		req := context.MustNewRequest(newTestHTTPRequest("GET", path))
		route, _ := matchForTest(t, router, req)
		if route != nil {
			t.Fatalf("非法自动路由片段 %q 不应生成路由，实际处理器为 %#v", path, route.Handler())
		}
	}
}

// TestAutoRouteSupportsWriteMethods 验证 ThinkPHP 自动调度不按请求方法改变控制器动作。
func TestAutoRouteSupportsWriteMethods(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(true); err != nil {
		t.Fatalf("启用自动路由失败: %v", err)
	}
	req := context.MustNewRequest(newTestHTTPRequest(http.MethodPost, "/user/save"))
	matched, _, err := router.Match(req)
	if err != nil || matched == nil || matched.Handler() != "user/save" {
		t.Fatalf("POST 自动路由应解析 user/save，route=%#v err=%v", matched, err)
	}
}

// newTestHTTPRequest 创建测试用的 HTTP 请求。
func newTestHTTPRequest(method, path string) *http.Request {
	req, _ := http.NewRequest(method, path, nil)
	return req
}
