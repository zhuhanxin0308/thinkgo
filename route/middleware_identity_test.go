package route

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// 禁止内联，保证测试中的两个闭包来自同一个生成位置。
//
//go:noinline
func middlewareGuardForStatus(status int) middleware.Handler {
	return func(_ *fwcontext.Request, _ func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return fwcontext.NewResponse().Code(status)
	}
}

func TestWithoutMiddlewareCannotRemoveDifferentCapturedHandler(t *testing.T) {
	router := NewRouter()
	protected, err := router.Get("/account", "Account@Show", middlewareGuardForStatus(http.StatusUnauthorized))
	if err != nil {
		t.Fatal(err)
	}
	if err := protected.WithoutMiddleware(middlewareGuardForStatus(http.StatusOK)); !errors.Is(err, ErrInvalidRouteMiddleware) {
		t.Errorf("从未注册的闭包不应被当作已注册的鉴权中间件: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test/account", nil))
	response := protected.ExecuteMiddleware(request, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Code(http.StatusOK)
	})
	if status := response.GetStatus(); status != http.StatusUnauthorized {
		t.Errorf("请求应保留鉴权结果，得到 HTTP %d", status)
	}
}

func TestNamedMiddlewareRemovalIsScopedAndAtomic(t *testing.T) {
	router := NewRouter()
	guard := middlewareGuardForStatus(http.StatusUnauthorized)
	second := middlewareGuardForStatus(http.StatusForbidden)
	var original, inherited *Route
	if err := router.Group("/api", func(base *Group) error {
		group, err := base.WithNamedMiddleware("guard", guard)
		if err != nil {
			return err
		}
		if _, err := group.WithNamedMiddleware(" guard ", second); !errors.Is(err, ErrInvalidRouteMiddleware) {
			t.Fatalf("派生分组不能重用中间件名称: %v", err)
		}
		original, err = base.Get("/public", "Account@Public")
		if err != nil {
			return err
		}
		inherited, err = group.Get("/private", "Account@Private")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := inherited.WithNamedMiddleware("invalid\nname", second); !errors.Is(err, ErrInvalidRouteMiddleware) {
		t.Fatalf("非法名称必须拒绝: %v", err)
	}
	if err := inherited.WithNamedMiddleware("second", nil); !errors.Is(err, ErrInvalidRouteMiddleware) {
		t.Fatalf("空处理器必须拒绝: %v", err)
	}
	if err := inherited.WithNamedMiddleware("second", second); err != nil {
		t.Fatal(err)
	}
	if err := inherited.WithoutNamedMiddleware("guard", "unknown"); !errors.Is(err, ErrInvalidRouteMiddleware) {
		t.Fatalf("未注册名称必须拒绝整次移除: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test/api/private", nil))
	destination := func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Code(http.StatusOK) }
	if status := original.ExecuteMiddleware(request, destination).GetStatus(); status != http.StatusOK {
		t.Fatalf("派生分组修改了原分组，实际状态 %d", status)
	}
	if status := inherited.ExecuteMiddleware(request, destination).GetStatus(); status != http.StatusUnauthorized {
		t.Fatalf("失败的批次移除了鉴权中间件，实际状态 %d", status)
	}
	if err := inherited.WithoutNamedMiddleware(" guard "); err != nil {
		t.Fatal(err)
	}
	if status := inherited.ExecuteMiddleware(request, destination).GetStatus(); status != http.StatusForbidden {
		t.Fatalf("按名称移除应仅删除指定中间件，实际状态 %d", status)
	}
	if err := router.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := inherited.WithoutNamedMiddleware("second"); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("冻结后不能移除中间件: %v", err)
	}
	if err := inherited.WithNamedMiddleware("late", guard); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("冻结后不能追加命名中间件: %v", err)
	}
}
