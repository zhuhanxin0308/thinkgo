package event

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type eventContextKey struct{}

type contextualEventListener struct {
	seenValue interface{}
	called    atomic.Int32
}

func (listener *contextualEventListener) Handle(event Event) error {
	return nil
}

func (listener *contextualEventListener) HandleContext(ctx context.Context, event Event) error {
	listener.seenValue = ctx.Value(eventContextKey{})
	listener.called.Add(1)
	return nil
}

// TestDispatcherDispatchContextPropagatesValues 验证上下文值会传递给可选的上下文监听器。
func TestDispatcherDispatchContextPropagatesValues(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &contextualEventListener{}
	if err := dispatcher.Listen("context.value", listener); err != nil {
		t.Fatalf("注册上下文监听器失败: %v", err)
	}
	ctx := context.WithValue(context.Background(), eventContextKey{}, "request-1")
	if err := dispatcher.DispatchContext(ctx, NewEvent("context.value", nil)); err != nil {
		t.Fatalf("上下文事件分发失败: %v", err)
	}
	if listener.called.Load() != 1 || listener.seenValue != "request-1" {
		t.Fatalf("上下文监听器未收到请求上下文: called=%d value=%#v", listener.called.Load(), listener.seenValue)
	}
}

// TestDispatcherDispatchContextStopsAfterCancellation 验证取消信号会阻止后续监听器继续执行。
func TestDispatcherDispatchContextStopsAfterCancellation(t *testing.T) {
	dispatcher := NewDispatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var secondCalled atomic.Int32
	if err := dispatcher.Listen("context.cancel", &SimpleListener{Handler: func(Event) error {
		cancel()
		return nil
	}}); err != nil {
		t.Fatalf("注册首个监听器失败: %v", err)
	}
	if err := dispatcher.Listen("context.cancel", &SimpleListener{Handler: func(Event) error {
		secondCalled.Add(1)
		return nil
	}}); err != nil {
		t.Fatalf("注册第二个监听器失败: %v", err)
	}
	if err := dispatcher.DispatchContext(ctx, NewEvent("context.cancel", nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后的事件分发应返回 context.Canceled，实际为 %v", err)
	}
	if secondCalled.Load() != 0 {
		t.Fatalf("上下文取消后不应执行第二个监听器，实际执行 %d 次", secondCalled.Load())
	}
}

// TestDispatcherDispatchContextRejectsNilContext 验证上下文 API 不接受 nil 上下文。
func TestDispatcherDispatchContextRejectsNilContext(t *testing.T) {
	dispatcher := NewDispatcher()
	var nilContext context.Context
	if err := dispatcher.DispatchContext(nilContext, NewEvent("context.nil", nil)); !errors.Is(err, ErrInvalidEventContext) {
		t.Fatalf("nil 上下文应返回 ErrInvalidEventContext，实际为 %v", err)
	}
}
