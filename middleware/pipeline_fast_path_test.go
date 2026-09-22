package middleware

import (
	stdcontext "context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestPipelineEmptyFastPathPreservesTerminatorDelta 验证空管道仍按本次执行范围返回新增终结回调。
func TestPipelineEmptyFastPathPreservesTerminatorDelta(t *testing.T) {
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
	existing := func(*fwcontext.Request, *fwcontext.Response) {}
	added := func(*fwcontext.Request, *fwcontext.Response) {}
	request.Set(requestTerminatorsKey, []Terminator{existing})

	response, terminators := NewPipeline().ThenWithTerminators(request, func(current *fwcontext.Request) *fwcontext.Response {
		current.Set(requestTerminatorsKey, append(RequestTerminators(current), added))
		return fwcontext.NewResponse()
	})
	if response == nil {
		t.Fatal("空管道应返回目标响应")
	}
	if len(terminators) != 1 || terminators[0] == nil {
		t.Fatalf("空管道应只返回目标新增终结回调，实际为 %#v", terminators)
	}
}

// TestPipelineIgnoresNilHandlers 验证无效中间件不会进入管道并在请求执行时触发 panic。
func TestPipelineIgnoresNilHandlers(t *testing.T) {
	pipeline := NewPipeline()
	pipeline.Pipe(nil).Unshift(nil).PipeLifecycle(nil, func(*fwcontext.Request, *fwcontext.Response) {})
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
	response := pipeline.Then(request, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Code(http.StatusNoContent)
	})
	if response == nil || response.GetStatus() != http.StatusNoContent {
		t.Fatalf("忽略空中间件后应正常执行目标，实际响应为 %#v", response)
	}
}

// TestPipelinePreservesMixedTerminationCallbacks 验证新旧终结回调可以共存，
// 旧公共入口仍返回兼容回调，而新快照入口会把有界上下文传给新回调。
func TestPipelinePreservesMixedTerminationCallbacks(t *testing.T) {
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
	response := fwcontext.NewResponse()
	order := make([]string, 0, 2)
	contextSawDeadline := false
	pipeline := NewPipeline().
		PipeLifecycle(
			func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				return next(request)
			},
			func(*fwcontext.Request, *fwcontext.Response) {
				order = append(order, "legacy")
			},
		).
		PipeLifecycleContext(
			func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				return next(request)
			},
			func(ctx stdcontext.Context, _ *fwcontext.Request, _ *fwcontext.Response) {
				_, contextSawDeadline = ctx.Deadline()
				order = append(order, "context")
			},
		)

	actualResponse, legacyCallbacks := pipeline.ThenWithTerminators(request, func(*fwcontext.Request) *fwcontext.Response {
		return response
	})
	if actualResponse != response || len(legacyCallbacks) != 2 {
		t.Fatalf("旧管道入口应保留两个兼容回调: response=%p callbacks=%d", actualResponse, len(legacyCallbacks))
	}
	callbacks := RequestTerminationCallbacks(request)
	if len(callbacks) != 2 {
		t.Fatalf("新快照入口应保留两个原始回调，实际为 %d", len(callbacks))
	}
	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
	defer cancel()
	for _, callback := range callbacks {
		callback.Invoke(ctx, request, response)
	}
	if len(order) != 2 || order[0] != "legacy" || order[1] != "context" {
		t.Fatalf("混合终结回调顺序错误: %#v", order)
	}
	if !contextSawDeadline {
		t.Fatal("ContextTerminator 未收到调用方的有界上下文")
	}
}

// TestThenHandlersPreservesDynamicPipelineSemantics 验证动态执行入口保留空、单个、多级和替换请求语义。
func TestThenHandlersPreservesDynamicPipelineSemantics(t *testing.T) {
	firstRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/first", nil))
	secondRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/second", nil))
	order := make([]string, 0, 3)
	handlers := []Handler{
		func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			if request != firstRequest {
				t.Fatalf("第一个动态中间件收到错误请求对象")
			}
			order = append(order, "first")
			return next(secondRequest)
		},
		func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			if request != secondRequest {
				t.Fatalf("动态中间件未保留替换后的请求对象")
			}
			order = append(order, "second")
			return next(request)
		},
	}
	response := ThenHandlers(firstRequest, handlers, func(request *fwcontext.Request) *fwcontext.Response {
		if request != secondRequest {
			t.Fatalf("动态管道目标未收到替换后的请求对象")
		}
		order = append(order, "destination")
		return fwcontext.NewResponse()
	})
	if response == nil || len(order) != 3 || order[0] != "first" || order[1] != "second" || order[2] != "destination" {
		t.Fatalf("动态管道执行结果错误: response=%#v order=%#v", response, order)
	}

	if response = ThenHandlers(firstRequest, nil, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse()
	}); response == nil {
		t.Fatal("空动态管道应执行目标")
	}
	if response = ThenHandlers(firstRequest, []Handler{
		func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			return next(request)
		},
	}, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse()
	}); response == nil {
		t.Fatal("单个动态中间件应执行目标")
	}
}

// BenchmarkPipelineExecution 对比固定管道和动态处理函数切片的热路径分配。
func BenchmarkPipelineExecution(b *testing.B) {
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
	destination := func(*fwcontext.Request) *fwcontext.Response { return nil }
	handlers := make([]Handler, 4)
	for index := range handlers {
		handlers[index] = func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			return next(request)
		}
	}
	b.Run("pipeline", func(b *testing.B) {
		pipeline := NewPipeline()
		for _, handler := range handlers {
			pipeline.Pipe(handler)
		}
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			pipeline.Then(request, destination)
		}
	})
	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			ThenHandlers(request, handlers, destination)
		}
	})
}
