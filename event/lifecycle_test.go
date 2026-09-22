package event

import (
	"testing"
)

// TestLifecycleEventNames 验证生命周期事件的名称常量正确性。
func TestLifecycleEventNames(t *testing.T) {
	cases := []struct {
		event    Event
		expected string
	}{
		{NewAppInitEvent(), EventAppInit},
		{NewHttpRunEvent(), EventHttpRun},
		{NewHttpEndEvent(200), EventHttpEnd},
		{NewRouteLoadedEvent(), EventRouteLoaded},
	}
	for _, tc := range cases {
		if tc.event.Name() != tc.expected {
			t.Errorf("事件名称不正确，期望 %q，实际 %q", tc.expected, tc.event.Name())
		}
	}
}

// TestHttpEndEventCarriesStatusCode 验证 HttpEnd 事件携带状态码。
func TestHttpEndEventCarriesStatusCode(t *testing.T) {
	evt := NewHttpEndEvent(404)
	if evt.StatusCode != 404 {
		t.Fatalf("HttpEndEvent 状态码不正确，期望 404，实际 %d", evt.StatusCode)
	}
}

// TestLifecycleEventsDispatch 验证生命周期事件可以正常通过 Dispatcher 分发和监听。
func TestLifecycleEventsDispatch(t *testing.T) {
	dispatcher := NewDispatcher()

	var appInitFired bool
	var httpRunFired bool
	var httpEndStatus int

	if err := dispatcher.Listen(EventAppInit, &SimpleListener{Handler: func(e Event) error {
		appInitFired = true
		return nil
	}}); err != nil {
		t.Fatalf("注册 AppInit 监听器失败: %v", err)
	}

	if err := dispatcher.Listen(EventHttpRun, &SimpleListener{Handler: func(e Event) error {
		httpRunFired = true
		return nil
	}}); err != nil {
		t.Fatalf("注册 HttpRun 监听器失败: %v", err)
	}

	if err := dispatcher.Listen(EventHttpEnd, &SimpleListener{Handler: func(e Event) error {
		if endEvent, ok := e.(*HttpEndEvent); ok {
			httpEndStatus = endEvent.StatusCode
		}
		return nil
	}}); err != nil {
		t.Fatalf("注册 HttpEnd 监听器失败: %v", err)
	}

	if err := dispatcher.Dispatch(NewAppInitEvent()); err != nil {
		t.Fatalf("分发 AppInit 事件失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewHttpRunEvent()); err != nil {
		t.Fatalf("分发 HttpRun 事件失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewHttpEndEvent(200)); err != nil {
		t.Fatalf("分发 HttpEnd 事件失败: %v", err)
	}

	if !appInitFired {
		t.Fatal("AppInit 事件未触发")
	}
	if !httpRunFired {
		t.Fatal("HttpRun 事件未触发")
	}
	if httpEndStatus != 200 {
		t.Fatalf("HttpEnd 事件状态码不正确，期望 200，实际 %d", httpEndStatus)
	}
}
