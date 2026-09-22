package event

import (
	"errors"
	"testing"
)

type panickingEvent struct{}

func (panickingEvent) Name() string { panic("event name panic") }

type panickingSubscriber struct{}

func (*panickingSubscriber) Subscribe(dispatcher *Dispatcher) error { panic("subscriber panic") }

// TestDispatcherZeroValueAndInvalidInputs 验证零值可用且 nil、空名称、畸形通配符会被拒绝。
func TestDispatcherZeroValueAndInvalidInputs(t *testing.T) {
	var dispatcher Dispatcher
	listener := &SimpleListener{}
	if err := dispatcher.Listen("user.created", listener); err != nil {
		t.Fatalf("Dispatcher 零值注册失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("user.created", nil)); err != nil {
		t.Fatalf("Dispatcher 零值分发失败: %v", err)
	}

	var nilListener *SimpleListener
	if err := dispatcher.Listen("user.created", nilListener); !errors.Is(err, ErrInvalidListener) {
		t.Fatalf("类型化 nil 监听器应返回 ErrInvalidListener，实际为 %v", err)
	}
	if err := dispatcher.Listen("", listener); !errors.Is(err, ErrInvalidEventName) {
		t.Fatalf("空事件名应返回 ErrInvalidEventName，实际为 %v", err)
	}
	if err := dispatcher.Listen("user.[", listener); !errors.Is(err, ErrInvalidEventName) {
		t.Fatalf("畸形通配符应返回 ErrInvalidEventName，实际为 %v", err)
	}
	if err := dispatcher.Listen("user.created", listener, true, false); !errors.Is(err, ErrInvalidListener) {
		t.Fatalf("多个首位参数应返回 ErrInvalidListener，实际为 %v", err)
	}
	if err := dispatcher.Dispatch(nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("nil 事件应返回 ErrInvalidEvent，实际为 %v", err)
	}
}

// TestDispatcherConvertsCallbackPanicsToErrors 验证事件、监听器和订阅者 panic 不会击穿应用进程。
func TestDispatcherConvertsCallbackPanicsToErrors(t *testing.T) {
	dispatcher := NewDispatcher()
	if err := dispatcher.Listen("panic", &SimpleListener{Handler: func(Event) error { panic("listener panic") }}); err != nil {
		t.Fatalf("注册 panic 监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("panic", nil)); !errors.Is(err, ErrEventCallbackPanic) {
		t.Fatalf("监听器 panic 应返回 ErrEventCallbackPanic，实际为 %v", err)
	}
	if err := dispatcher.Dispatch(panickingEvent{}); !errors.Is(err, ErrEventCallbackPanic) {
		t.Fatalf("事件 Name panic 应返回 ErrEventCallbackPanic，实际为 %v", err)
	}
	if err := dispatcher.Subscribe(&panickingSubscriber{}); !errors.Is(err, ErrEventCallbackPanic) {
		t.Fatalf("订阅者 panic 应返回 ErrEventCallbackPanic，实际为 %v", err)
	}
	listenerErr := errors.New("listener failed")
	errorDispatcher := NewDispatcher()
	if err := errorDispatcher.Listen("failed", &SimpleListener{Handler: func(Event) error { return listenerErr }}); err != nil {
		t.Fatalf("注册失败监听器失败: %v", err)
	}
	if err := errorDispatcher.Dispatch(NewEvent("failed", nil)); !errors.Is(err, listenerErr) {
		t.Fatalf("监听器返回错误应传播给调用方，实际为 %v", err)
	}
}

// TestDispatcherForgetRemovesExactAndWildcardListeners 验证动态监听器可释放，避免长期进程持续累积。
func TestDispatcherForgetRemovesExactAndWildcardListeners(t *testing.T) {
	dispatcher := NewDispatcher()
	called := 0
	listener := &SimpleListener{Handler: func(Event) error {
		called++
		return nil
	}}
	if err := dispatcher.Listen("user.created", listener); err != nil {
		t.Fatalf("注册精确监听器失败: %v", err)
	}
	if err := dispatcher.Listen("user.*", listener); err != nil {
		t.Fatalf("注册通配监听器失败: %v", err)
	}
	if err := dispatcher.Forget("user.created"); err != nil {
		t.Fatalf("移除精确监听器失败: %v", err)
	}
	if err := dispatcher.Forget("user.*"); err != nil {
		t.Fatalf("移除通配监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("user.created", nil)); err != nil {
		t.Fatalf("分发清理后的事件失败: %v", err)
	}
	if called != 0 {
		t.Fatalf("已移除监听器不应执行，实际调用 %d 次", called)
	}
}
