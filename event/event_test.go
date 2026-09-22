package event

import (
	"testing"
	"time"
)

// TestDispatchAllowsListenerMutation 验证监听器在处理事件时再次注册监听器不会死锁。
func TestDispatchAllowsListenerMutation(t *testing.T) {
	dispatcher := NewDispatcher()
	done := make(chan error, 1)

	if err := dispatcher.Listen("user.created", &SimpleListener{
		Handler: func(event Event) error {
			return dispatcher.Listen("user.created", &SimpleListener{})
		},
	}); err != nil {
		t.Fatalf("注册测试监听器失败: %v", err)
	}

	go func() {
		done <- dispatcher.Dispatch(NewEvent("user.created", nil))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("监听器内部注册失败: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("事件分发在监听器内部修改注册表时发生阻塞")
	}
}
