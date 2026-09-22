package http

import (
	stdcontext "context"
	stdhttp "net/http"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
)

const requestEndAllocationSamples = 100

// TestRequestEndWithoutListenersDoesNotAllocate 防止未使用事件的普通请求仍创建事件及载荷对象。
func TestRequestEndWithoutListenersDoesNotAllocate(t *testing.T) {
	for _, test := range []struct {
		name       string
		dispatcher *event.Dispatcher
	}{
		{name: "未配置事件"},
		{name: "没有监听器", dispatcher: event.NewDispatcher()},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &Http{event: test.dispatcher}
			assertRequestEndWithoutAllocation(t, handler)
		})
	}
}

// TestRequestEndAllocationTracksDynamicListeners 验证优化不会缓存监听状态，动态增删后立即生效。
func TestRequestEndAllocationTracksDynamicListeners(t *testing.T) {
	dispatcher := event.NewDispatcher()
	handler := &Http{event: dispatcher}
	assertRequestEndWithoutAllocation(t, handler)
	response := fwcontext.NewResponse().Code(stdhttp.StatusCreated)
	calls := 0
	if err := dispatcher.Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(current event.Event) error {
		ended, ok := current.(*event.HttpEndEvent)
		if !ok || ended.StatusCode != stdhttp.StatusCreated || ended.Data != response {
			t.Fatalf("动态监听器应收到当前完整响应: %#v", current)
		}
		calls++
		return nil
	}}); err != nil {
		t.Fatalf("添加监听器失败: %v", err)
	}
	if err := handler.runRequestEndLifecycle(stdcontext.Background(), response, nil); err != nil {
		t.Fatalf("事件收尾失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("监听器应执行一次，实际 %d", calls)
	}
	if err := dispatcher.Remove(event.EventHttpEnd); err != nil {
		t.Fatalf("移除监听器失败: %v", err)
	}
	assertRequestEndWithoutAllocation(t, handler)
	if calls != 1 {
		t.Fatalf("移除后的监听器不应继续执行，实际 %d", calls)
	}
}

func assertRequestEndWithoutAllocation(t *testing.T, handler *Http) {
	t.Helper()
	response := fwcontext.NewResponse()
	allocations := testing.AllocsPerRun(requestEndAllocationSamples, func() {
		if err := handler.runRequestEndLifecycle(stdcontext.Background(), response, nil); err != nil {
			t.Fatalf("无扩展收尾失败: %v", err)
		}
	})
	if allocations != 0 {
		t.Fatalf("无监听器收尾不应分配，实际 %.2f 次", allocations)
	}
}
