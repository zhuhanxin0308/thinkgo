package route

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// TestRouteCommitFailureCanRetry 验证关联契约失败不会占用路由，重试接收完整分组信息。
func TestRouteCommitFailureCanRetry(t *testing.T) {
	router := NewRouter()
	rejected := errors.New("契约冲突")
	handler := func() string { return "ok" }
	mw := func(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
		return next(request)
	}
	err := router.Group("/api", func(group *Group) error {
		if _, err := group.AddWithCommit(http.MethodGet, "/items/:id", handler, func(info RouteInfo) error {
			if info.Path != "/api/items/:id" || info.Method != http.MethodGet || info.MiddlewareCount != 1 {
				t.Fatalf("契约未收到最终路由: %#v", info)
			}
			return rejected
		}); !errors.Is(err, rejected) {
			t.Fatalf("关联失败未保留: %v", err)
		}
		_, err := group.AddWithCommit(http.MethodGet, "/items/:id", handler, func(RouteInfo) error { return nil })
		return err
	}, middleware.Handler(mw))
	if err != nil {
		t.Fatal(err)
	}
	routes, err := router.Routes()
	if err != nil || len(routes) != 1 {
		t.Fatalf("失败留下了路由: %#v %v", routes, err)
	}
}

// TestRouteCommitOnlyAfterValidation 验证无效、重复和冻结路由不会发布关联契约。
func TestRouteCommitOnlyAfterValidation(t *testing.T) {
	router := NewRouter()
	called := 0
	commit := func(RouteInfo) error { called++; return nil }
	if _, err := router.AddWithCommit(http.MethodGet, "/items", nil, commit); err == nil || called != 0 {
		t.Fatalf("非法处理器执行了提交: %d %v", called, err)
	}
	if _, err := router.AddWithCommit(http.MethodGet, "/items", func() {}, nil); err == nil {
		t.Fatal("空提交回调应返回错误")
	}
	if _, err := router.AddWithCommit(http.MethodGet, "/items", func() {}, commit); err != nil {
		t.Fatal(err)
	}
	if _, err := router.AddWithCommit(http.MethodGet, "/items", func() {}, commit); !errors.Is(err, ErrDuplicateRoute) || called != 1 {
		t.Fatalf("重复路由发布了契约: %d %v", called, err)
	}
	if err := router.Freeze(); err != nil {
		t.Fatal(err)
	}
	if _, err := router.AddWithCommit(http.MethodGet, "/other", func() {}, commit); !errors.Is(err, ErrRouterFrozen) || called != 1 {
		t.Fatalf("冻结路由发布了契约: %d %v", called, err)
	}
	var absent *Router
	if _, err := absent.AddWithCommit(http.MethodGet, "/items", func() {}, commit); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("空路由器错误: %v", err)
	}
	var group *Group
	if _, err := group.AddWithCommit(http.MethodGet, "/items", func() {}, commit); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("空分组错误: %v", err)
	}
}

// TestConcurrentRouteCommitPublishesOnce 验证并发重复注册只有胜出的路由能提交关联状态。
func TestConcurrentRouteCommitPublishesOnce(t *testing.T) {
	const registrations = 16
	router := NewRouter()
	var commits atomic.Int32
	var successes atomic.Int32
	var wait sync.WaitGroup
	for range registrations {
		wait.Go(func() {
			_, err := router.AddWithCommit(http.MethodGet, "/items", func() {}, func(RouteInfo) error {
				commits.Add(1)
				return nil
			})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrDuplicateRoute) {
				t.Errorf("并发注册错误: %v", err)
			}
		})
	}
	wait.Wait()
	if successes.Load() != 1 || commits.Load() != 1 {
		t.Fatalf("提交次数与路由不一致: successes=%d commits=%d", successes.Load(), commits.Load())
	}
}
