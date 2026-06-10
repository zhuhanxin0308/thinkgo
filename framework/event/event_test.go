package event

import (
	"testing"
	"time"
)

// TestDispatchAllowsListenerMutation 验证监听器在处理事件时再次注册监听器不会死锁。
func TestDispatchAllowsListenerMutation(t *testing.T) {
	dispatcher := NewDispatcher()
	done := make(chan struct{})

	dispatcher.Listen("user.created", &SimpleListener{
		Handler: func(event Event) {
			dispatcher.Listen("user.created", &SimpleListener{})
			close(done)
		},
	})

	go dispatcher.Dispatch(NewEvent("user.created", nil))

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("事件分发在监听器内部修改注册表时发生阻塞")
	}
}
