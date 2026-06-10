package event

import "testing"

type testSubscriber struct {
	order *[]string
}

func (s *testSubscriber) Subscribe(dispatcher *Dispatcher) {
	dispatcher.Listen("order.created", &SimpleListener{
		Handler: func(event Event) {
			*s.order = append(*s.order, "subscriber")
		},
	}, 5)
}

func TestDispatcherPriorityWildcardAndSubscriber(t *testing.T) {
	order := make([]string, 0)
	dispatcher := NewDispatcher()

	dispatcher.Listen("user.created", &SimpleListener{
		Handler: func(event Event) {
			order = append(order, "low")
		},
	}, 1)
	dispatcher.Listen("user.*", &SimpleListener{
		Handler: func(event Event) {
			order = append(order, "wildcard")
		},
	}, 10)
	dispatcher.Subscribe(&testSubscriber{order: &order})

	dispatcher.Dispatch(NewEvent("user.created", nil))
	if len(order) != 2 || order[0] != "wildcard" || order[1] != "low" {
		t.Fatalf("优先级或通配监听执行顺序不正确，实际为 %#v", order)
	}

	dispatcher.Dispatch(NewEvent("order.created", nil))
	if len(order) != 3 || order[2] != "subscriber" {
		t.Fatalf("Subscriber 未生效，实际为 %#v", order)
	}
}

func TestDispatcherStopPropagation(t *testing.T) {
	dispatcher := NewDispatcher()
	called := make([]string, 0)

	dispatcher.Listen("user.created", &SimpleListener{
		Handler: func(event Event) {
			called = append(called, "first")
			if stoppable, ok := event.(interface{ StopPropagation() }); ok {
				stoppable.StopPropagation()
			}
		},
	}, 10)
	dispatcher.Listen("user.created", &SimpleListener{
		Handler: func(event Event) {
			called = append(called, "second")
		},
	}, 1)

	dispatcher.Dispatch(NewEvent("user.created", nil))
	if len(called) != 1 || called[0] != "first" {
		t.Fatalf("StopPropagation 应阻止后续监听器执行，实际为 %#v", called)
	}
}
