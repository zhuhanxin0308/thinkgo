package event

import "testing"

type testSubscriber struct {
	order *[]string
}

func (s *testSubscriber) Subscribe(dispatcher *Dispatcher) error {
	return dispatcher.ListenPriority("order.created", &SimpleListener{
		Handler: func(event Event) error {
			*s.order = append(*s.order, "subscriber")
			return nil
		},
	}, 5)
}

func TestDispatcherPriorityWildcardAndSubscriber(t *testing.T) {
	order := make([]string, 0)
	dispatcher := NewDispatcher()

	if err := dispatcher.ListenPriority("user.created", &SimpleListener{
		Handler: func(event Event) error {
			order = append(order, "low")
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("注册低优先级监听器失败: %v", err)
	}
	if err := dispatcher.ListenPriority("user.*", &SimpleListener{
		Handler: func(event Event) error {
			order = append(order, "wildcard")
			return nil
		},
	}, 10); err != nil {
		t.Fatalf("注册通配监听器失败: %v", err)
	}
	if err := dispatcher.Subscribe(&testSubscriber{order: &order}); err != nil {
		t.Fatalf("注册订阅者失败: %v", err)
	}

	if err := dispatcher.Dispatch(NewEvent("user.created", nil)); err != nil {
		t.Fatalf("分发用户事件失败: %v", err)
	}
	if len(order) != 2 || order[0] != "wildcard" || order[1] != "low" {
		t.Fatalf("优先级或通配监听执行顺序不正确，实际为 %#v", order)
	}

	if err := dispatcher.Dispatch(NewEvent("order.created", nil)); err != nil {
		t.Fatalf("分发订单事件失败: %v", err)
	}
	if len(order) != 3 || order[2] != "subscriber" {
		t.Fatalf("Subscriber 未生效，实际为 %#v", order)
	}
}

func TestDispatcherStopPropagation(t *testing.T) {
	dispatcher := NewDispatcher()
	called := make([]string, 0)

	if err := dispatcher.ListenPriority("user.created", &SimpleListener{
		Handler: func(event Event) error {
			called = append(called, "first")
			if stoppable, ok := event.(interface{ StopPropagation() }); ok {
				stoppable.StopPropagation()
			}
			return nil
		},
	}, 10); err != nil {
		t.Fatalf("注册停止传播监听器失败: %v", err)
	}
	if err := dispatcher.ListenPriority("user.created", &SimpleListener{
		Handler: func(event Event) error {
			called = append(called, "second")
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("注册后续监听器失败: %v", err)
	}

	if err := dispatcher.Dispatch(NewEvent("user.created", nil)); err != nil {
		t.Fatalf("分发停止传播事件失败: %v", err)
	}
	if len(called) != 1 || called[0] != "first" {
		t.Fatalf("StopPropagation 应阻止后续监听器执行，实际为 %#v", called)
	}
}
