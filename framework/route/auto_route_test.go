package route

import (
	"net/http"
	"testing"

	"thinkgo/framework/context"
)

// TestAutoRouteDisabledByDefault 验证自动路由默认关闭。
func TestAutoRouteDisabledByDefault(t *testing.T) {
	router := NewRouter()
	req := context.NewRequest(newTestHTTPRequest("GET", "/user/index"))

	route, _ := router.Match(req)
	if route != nil {
		t.Fatal("自动路由默认应关闭，未注册的路由不应匹配")
	}
}

// TestAutoRouteResolvesControllerAction 验证自动路由将 /user/edit 解析为 User@Edit。
func TestAutoRouteResolvesControllerAction(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.NewRequest(newTestHTTPRequest("GET", "/user/edit"))
	route, _ := router.Match(req)

	if route == nil {
		t.Fatal("自动路由应匹配 /user/edit")
	}
	if handler, ok := route.Handler.(string); !ok || handler != "User@Edit" {
		t.Fatalf("自动路由应解析为 User@Edit，实际为 %v", route.Handler)
	}
}

// TestAutoRouteDefaultAction 验证 /user 映射到默认动作。
func TestAutoRouteDefaultAction(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.NewRequest(newTestHTTPRequest("GET", "/user"))
	route, _ := router.Match(req)

	if route == nil {
		t.Fatal("自动路由应匹配 /user")
	}
	if handler, ok := route.Handler.(string); !ok || handler != "User@Index" {
		t.Fatalf("自动路由应解析为 User@Index，实际为 %v", route.Handler)
	}
}

// TestAutoRouteRootPath 验证根路径映射到默认控制器默认动作。
func TestAutoRouteRootPath(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.NewRequest(newTestHTTPRequest("GET", "/"))
	// 注意：根路径如果有注册路由会先匹配注册路由
	route, _ := router.Match(req)

	if route == nil {
		t.Fatal("自动路由应匹配 /")
	}
	if handler, ok := route.Handler.(string); !ok || handler != "Index@Index" {
		t.Fatalf("自动路由应解析为 Index@Index，实际为 %v", route.Handler)
	}
}

// TestAutoRouteMultiLevel 验证多层级路径 /admin/user/edit → Admin.User@Edit。
func TestAutoRouteMultiLevel(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)

	req := context.NewRequest(newTestHTTPRequest("GET", "/admin/user/edit"))
	route, _ := router.Match(req)

	if route == nil {
		t.Fatal("自动路由应匹配 /admin/user/edit")
	}
	if handler, ok := route.Handler.(string); !ok || handler != "Admin.User@Edit" {
		t.Fatalf("自动路由应解析为 Admin.User@Edit，实际为 %v", route.Handler)
	}
}

// TestAutoRouteExplicitRoutePriority 验证显式路由优先于自动路由。
func TestAutoRouteExplicitRoutePriority(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)
	router.Get("/user/edit", "CustomController@CustomAction")

	req := context.NewRequest(newTestHTTPRequest("GET", "/user/edit"))
	route, _ := router.Match(req)

	if route == nil {
		t.Fatal("应匹配显式路由")
	}
	if handler, ok := route.Handler.(string); !ok || handler != "CustomController@CustomAction" {
		t.Fatalf("显式路由应优先于自动路由，实际为 %v", route.Handler)
	}
}

// TestAutoRouteCustomDefault 验证自定义默认控制器和动作。
func TestAutoRouteCustomDefault(t *testing.T) {
	router := NewRouter()
	router.EnableAutoRoute(true)
	router.SetDefaultController("Home")
	router.SetDefaultAction("main")

	req := context.NewRequest(newTestHTTPRequest("GET", "/"))
	route, _ := router.Match(req)

	if route == nil {
		t.Fatal("自动路由应匹配 /")
	}
	if handler, ok := route.Handler.(string); !ok || handler != "Home@Main" {
		t.Fatalf("自定义默认应解析为 Home@Main，实际为 %v", route.Handler)
	}
}

// newTestHTTPRequest 创建测试用的 HTTP 请求。
func newTestHTTPRequest(method, path string) *http.Request {
	req, _ := http.NewRequest(method, path, nil)
	return req
}
