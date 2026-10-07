package event_test

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/event"
)

func ExampleDispatcher_HasApplicationListener() {
	dispatcher := event.NewDispatcher()
	listener := &event.SimpleListener{Handler: func(event.Event) error { return nil }}
	if err := dispatcher.ListenApplicationEvents(map[string][]event.Listener{"HttpRun": {listener}}); err != nil {
		panic(err)
	}
	fmt.Println("any listener:", dispatcher.HasListener("HttpRun"))
	fmt.Println("application listener:", dispatcher.HasApplicationListener("HttpRun"))
	// 应用级匹配不表示 DispatchProjectContext 会执行该监听器。
	// Output:
	// any listener: true
	// application listener: true
}
