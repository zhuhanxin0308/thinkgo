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
		{NewLogWriteEvent("file"), EventLogWrite},
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

// TestLogWriteEventCarriesChannel 验证 LogWrite 事件携带频道名。
func TestLogWriteEventCarriesChannel(t *testing.T) {
	evt := NewLogWriteEvent("file")
	if evt.Channel != "file" {
		t.Fatalf("LogWriteEvent 频道不正确，期望 file，实际 %s", evt.Channel)
	}
}

// TestLifecycleEventsDispatch 验证生命周期事件可以正常通过 Dispatcher 分发和监听。
func TestLifecycleEventsDispatch(t *testing.T) {
	dispatcher := NewDispatcher()

	var appInitFired bool
	var httpRunFired bool
	var httpEndStatus int

	dispatcher.Listen(EventAppInit, &SimpleListener{Handler: func(e Event) {
		appInitFired = true
	}})

	dispatcher.Listen(EventHttpRun, &SimpleListener{Handler: func(e Event) {
		httpRunFired = true
	}})

	dispatcher.Listen(EventHttpEnd, &SimpleListener{Handler: func(e Event) {
		if endEvent, ok := e.(*HttpEndEvent); ok {
			httpEndStatus = endEvent.StatusCode
		}
	}})

	dispatcher.Dispatch(NewAppInitEvent())
	dispatcher.Dispatch(NewHttpRunEvent())
	dispatcher.Dispatch(NewHttpEndEvent(200))

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
