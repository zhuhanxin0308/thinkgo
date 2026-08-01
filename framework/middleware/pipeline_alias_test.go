package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"thinkgo/framework/context"
)

// TestPipeByNameStrictPreservesLegacyAliasBehavior 验证旧别名入口继续静默兼容，严格入口只在缺失时失败。
func TestPipeByNameStrictPreservesLegacyAliasBehavior(t *testing.T) {
	pipeline := NewPipeline()
	if pipeline.PipeByName("missing") != pipeline || len(pipeline.pipes) != 0 {
		t.Fatal("旧别名入口遇到缺失别名时应保持静默且不改变管道")
	}
	if err := pipeline.PipeByNameStrict("missing"); !errors.Is(err, ErrMiddlewareAliasNotFound) {
		t.Fatalf("严格别名入口应返回 ErrMiddlewareAliasNotFound，实际为 %v", err)
	}

	var order []string
	pipeline.Alias("first", func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		order = append(order, "first")
		return next(req)
	})
	pipeline.Alias("second", func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		order = append(order, "second")
		return next(req)
	})
	pipeline.SetPriority([]string{"second", "first"})
	if err := pipeline.PipeByNameStrict("first"); err != nil {
		t.Fatalf("注册 first 别名失败: %v", err)
	}
	if err := pipeline.PipeByNameStrict("second"); err != nil {
		t.Fatalf("注册 second 别名失败: %v", err)
	}
	response := pipeline.Then(context.MustNewRequest(httptestRequest()), func(*context.Request) *context.Response {
		return context.NewResponse().Code(http.StatusNoContent)
	})
	if response == nil || response.GetStatus() != http.StatusNoContent {
		t.Fatalf("别名管道响应错误: %#v", response)
	}
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("严格别名注册不应改变优先级顺序: %#v", order)
	}
}

// TestPipelinePriorityConcurrentFirstRequest 验证启用优先级时首批并发请求不会竞争排序缓存。
func TestPipelinePriorityConcurrentFirstRequest(t *testing.T) {
	pipeline := NewPipeline()
	pass := func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		return next(req)
	}
	pipeline.Alias("first", pass).Alias("second", pass).SetPriority([]string{"second", "first"})
	if err := pipeline.PipeByNameStrict("first"); err != nil {
		t.Fatalf("注册 first 失败: %v", err)
	}
	if err := pipeline.PipeByNameStrict("second"); err != nil {
		t.Fatalf("注册 second 失败: %v", err)
	}

	const workers = 64
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			request := context.MustNewRequest(httptestRequest())
			response := pipeline.Then(request, func(*context.Request) *context.Response {
				return context.NewResponse().Code(http.StatusNoContent)
			})
			if response == nil || response.GetStatus() != http.StatusNoContent {
				t.Errorf("并发首请求响应错误: %#v", response)
			}
		}()
	}
	waitGroup.Wait()
}

func httptestRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
}
