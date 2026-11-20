package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestZeroValuePipelineCanRegisterAndExecuteAliases 验证零值管道可完整配置、解析并执行别名中间件。
func TestZeroValuePipelineCanRegisterAndExecuteAliases(t *testing.T) {
	var pipeline Pipeline
	var order []string
	handler := func(name string) Handler {
		return func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			order = append(order, name)
			return next(request)
		}
	}

	pipeline.WrapOuter(handler("outer")).
		Unshift(handler("unshift")).
		Pipe(handler("pipe")).
		Alias(" auth ", handler("alias"))

	if pipeline.ResolveAlias("auth") == nil {
		t.Fatal("零值 Pipeline 注册别名后应能解析")
	}
	if err := pipeline.PipeByNameStrict("auth"); err != nil {
		t.Fatalf("零值 Pipeline 注册别名后应能通过严格入口引用: %v", err)
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	response := pipeline.Then(request, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Code(http.StatusNoContent)
	})
	if response == nil || response.GetStatus() != http.StatusNoContent {
		t.Fatalf("零值 Pipeline 执行结果错误: %#v", response)
	}
	if want := []string{"outer", "unshift", "pipe", "alias"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("零值 Pipeline 中间件执行顺序错误: got=%v want=%v", order, want)
	}
}

// TestZeroValuePipelineConcurrentAliasRegistration 验证零值映射惰性初始化受同一互斥锁保护。
func TestZeroValuePipelineConcurrentAliasRegistration(t *testing.T) {
	var pipeline Pipeline
	const workers = 32
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			pipeline.Alias(fmt.Sprintf("middleware-%d", index), func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				return next(request)
			})
		}()
	}
	wait.Wait()
	for index := range workers {
		if pipeline.ResolveAlias(fmt.Sprintf("middleware-%d", index)) == nil {
			t.Fatalf("并发注册的别名 middleware-%d 丢失", index)
		}
	}
}
