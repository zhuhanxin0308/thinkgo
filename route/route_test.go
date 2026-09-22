package route

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestMatchPathParametersAndConstraints(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/api/users/:userId/posts/:postId", "Post@Show")
	if err != nil {
		t.Fatalf("注册参数路由失败: %v", err)
	}
	if err = registered.WithPattern("userId", `[0-9]+`); err != nil {
		t.Fatalf("设置参数约束失败: %v", err)
	}

	matched, params := matchForTest(t, router, fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "http://example.com/api/users/10/posts/99", nil),
	))
	if matched == nil || params["userId"] != "10" || params["postId"] != "99" {
		t.Fatalf("参数提取错误，路由=%#v 参数=%#v", matched, params)
	}

	missed, _, err := router.Match(fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "http://example.com/api/users/alice/posts/99", nil),
	))
	if err != nil || missed != nil {
		t.Fatalf("不满足约束的参数不应匹配，路由=%#v 错误=%v", missed, err)
	}
}

func TestRouterFreezeBuildsMethodIndexes(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/api/users", "User@Index"); err != nil {
		t.Fatalf("注册 GET 路由失败: %v", err)
	}
	if _, err := router.Post("/api/users", "User@Create"); err != nil {
		t.Fatalf("注册 POST 路由失败: %v", err)
	}
	if _, err := router.Get("/api/users/:id", "User@Show"); err != nil {
		t.Fatalf("注册动态路由失败: %v", err)
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结路由失败: %v", err)
	}

	if len(router.staticRoutes[http.MethodGet][pathPartsKey([]string{"api", "users"})]) != 1 {
		t.Fatal("GET 静态路由未按路径和域名建立索引")
	}
	if len(router.staticRoutes[http.MethodPost][pathPartsKey([]string{"api", "users"})]) != 1 {
		t.Fatal("POST 静态路由未建立索引")
	}
	if len(router.dynamicRoutes[http.MethodGet]) != 1 {
		t.Fatal("GET 动态路由未建立方法索引")
	}
}

func TestMissRouteAndNilRequest(t *testing.T) {
	router := NewRouter()
	miss, err := router.Miss("Error@NotFound")
	if err != nil || miss == nil {
		t.Fatalf("注册 Miss 路由失败: %v", err)
	}
	matched, _, err := router.Match(fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "http://example.com/missing", nil),
	))
	if err != nil || matched != miss {
		t.Fatalf("未命中请求应返回 Miss 路由，路由=%#v 错误=%v", matched, err)
	}
	if _, _, err = router.Match(nil); !errors.Is(err, ErrNilRequest) {
		t.Fatalf("空请求必须返回 ErrNilRequest，实际为 %v", err)
	}
}
