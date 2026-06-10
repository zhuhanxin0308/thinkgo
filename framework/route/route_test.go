package route

import (
	"regexp"
	"testing"
)

// TestMatchPathExact 验证精确路径匹配
func TestMatchPathExact(t *testing.T) {
	route := &Route{
		Path:             "/api/users",
		compiledPatterns: make(map[string]*regexp.Regexp),
		patterns:         make(map[string]string),
	}
	matched, params := route.matchPath("/api/users")
	if !matched {
		t.Fatal("精确路径应匹配")
	}
	if params != nil {
		t.Fatal("精确匹配不应有参数")
	}
}

// TestMatchPathParams 验证路由参数提取
func TestMatchPathParams(t *testing.T) {
	route := &Route{
		Path:             "/api/users/:id",
		compiledPatterns: make(map[string]*regexp.Regexp),
		patterns:         make(map[string]string),
	}
	matched, params := route.matchPath("/api/users/42")
	if !matched {
		t.Fatal("参数路由应匹配")
	}
	if params == nil || params["id"] != "42" {
		t.Fatalf("路由参数提取不正确，期望 id=42，实际 %v", params)
	}
}

// TestMatchPathMultipleParams 验证多参数提取
func TestMatchPathMultipleParams(t *testing.T) {
	route := &Route{
		Path:             "/api/users/:userId/posts/:postId",
		compiledPatterns: make(map[string]*regexp.Regexp),
		patterns:         make(map[string]string),
	}
	matched, params := route.matchPath("/api/users/10/posts/99")
	if !matched {
		t.Fatal("多参数路由应匹配")
	}
	if params["userId"] != "10" {
		t.Fatalf("userId 参数不正确: %s", params["userId"])
	}
	if params["postId"] != "99" {
		t.Fatalf("postId 参数不正确: %s", params["postId"])
	}
}

// TestMatchPathMismatch 验证不匹配的路径
func TestMatchPathMismatch(t *testing.T) {
	route := &Route{
		Path:             "/api/users/:id",
		compiledPatterns: make(map[string]*regexp.Regexp),
		patterns:         make(map[string]string),
	}
	matched, _ := route.matchPath("/api/orders/42")
	if matched {
		t.Fatal("路径前缀不同，不应匹配")
	}
}

// TestMatchPathLengthMismatch 验证段数不同的路径
func TestMatchPathLengthMismatch(t *testing.T) {
	route := &Route{
		Path:             "/api/users/:id",
		compiledPatterns: make(map[string]*regexp.Regexp),
		patterns:         make(map[string]string),
	}
	matched, _ := route.matchPath("/api/users/42/extra")
	if matched {
		t.Fatal("段数不同，不应匹配")
	}
}

// TestStaticRouteIndex 验证静态路由快速索引
func TestStaticRouteIndex(t *testing.T) {
	router := NewRouter()
	router.Get("/api/users", "User@Index")
	router.Post("/api/users", "User@Create")

	// 验证静态路由索引已建立
	if _, ok := router.staticRoutes["GET"]["/api/users"]; !ok {
		t.Fatal("GET /api/users 未加入静态路由索引")
	}
	if _, ok := router.staticRoutes["POST"]["/api/users"]; !ok {
		t.Fatal("POST /api/users 未加入静态路由索引")
	}
}

// TestDynamicRouteNotInStaticIndex 验证动态路由不在静态索引中
func TestDynamicRouteNotInStaticIndex(t *testing.T) {
	router := NewRouter()
	router.Get("/api/users/:id", "User@Show")

	if _, ok := router.staticRoutes["GET"]; ok {
		if _, ok2 := router.staticRoutes["GET"]["/api/users/:id"]; ok2 {
			t.Fatal("动态路由不应加入静态索引")
		}
	}
}

// TestMissRoute 验证 404 兜底路由
func TestMissRoute(t *testing.T) {
	router := NewRouter()
	router.Get("/api/home", "Home@Index")
	router.Miss("Error@NotFound")

	if router.missRoute == nil {
		t.Fatal("Miss 路由未设置")
	}
}
