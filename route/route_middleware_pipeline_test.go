package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestRouteMiddlewarePipelineReuse 验证路由管道在注册阶段装配并保持中间件执行顺序。
func TestRouteMiddlewarePipelineReuse(t *testing.T) {
	order := make([]string, 0, 3)
	first := func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		order = append(order, "first")
		return next(request)
	}
	second := func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		order = append(order, "second")
		return next(request)
	}
	router := NewRouter()
	plain, err := router.Get("/plain", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("plain")
	})
	if err != nil {
		t.Fatalf("注册无中间件路由失败: %v", err)
	}
	registered, err := router.Get("/users", func(*fwcontext.Request) *fwcontext.Response {
		order = append(order, "destination")
		return fwcontext.NewResponse().Content("ok")
	}, first, second)
	if err != nil {
		t.Fatalf("注册带中间件路由失败: %v", err)
	}
	if plain.HasMiddleware() {
		t.Fatal("无中间件路由不应创建管道")
	}
	if !registered.HasMiddleware() {
		t.Fatal("带中间件路由应创建管道")
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users", nil))
	response := registered.ExecuteMiddleware(request, func(current *fwcontext.Request) *fwcontext.Response {
		return registered.Handler().(func(*fwcontext.Request) *fwcontext.Response)(current)
	})
	if response == nil || string(response.GetBody()) != "ok" {
		t.Fatalf("路由管道响应错误: %#v", response)
	}
	if got, want := len(order), 3; got != want || order[0] != "first" || order[1] != "second" || order[2] != "destination" {
		t.Fatalf("路由管道执行顺序错误: %#v", order)
	}

	if err := registered.WithoutMiddleware(first, second); err != nil {
		t.Fatalf("移除路由中间件失败: %v", err)
	}
	if registered.HasMiddleware() {
		t.Fatal("移除全部中间件后不应保留管道")
	}
	if len(registered.Middlewares()) != 0 {
		t.Fatal("移除全部中间件后快照不应保留处理函数")
	}
}
