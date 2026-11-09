package event

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

// TestBatchListenersRollback 验证失败批次完整回滚，已有监听、别名、序号与应用标记均保留。
func TestBatchListenersRollback(t *testing.T) {
	var typedNil *SimpleListener
	for _, application := range []bool{false, true} {
		for _, bad := range []Listener{nil, typedNil} {
			dispatcher := NewDispatcher()
			calls := 0
			listener := &SimpleListener{Handler: func(Event) error { calls++; return nil }}
			if err := dispatcher.Listen("order.created", listener); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.BindEvent("a.alias", func(data interface{}) Event { return NewEvent("order.created", data) }); err != nil {
				t.Fatal(err)
			}
			register := dispatcher.ListenEvents
			if application {
				register = dispatcher.ListenApplicationEvents
			}
			before := dispatcher.captureRegistrationState()
			batch := map[string][]Listener{"a.alias": {listener}, "b.*": {listener}, "z.invalid": {listener, bad}}
			if err := register(batch); err == nil {
				t.Fatal("含空监听器的批次必须失败")
			}
			after := dispatcher.captureRegistrationState()
			if after.sequence != before.sequence || !reflect.DeepEqual(after.listeners, before.listeners) || !reflect.DeepEqual(after.wildcards, before.wildcards) {
				t.Fatalf("失败批次泄漏注册状态: 应用级=%t, 前序号=%d, 后序号=%d", application, before.sequence, after.sequence)
			}
			if dispatcher.boundEventName("a.alias") != "order.created" {
				t.Fatal("失败批次破坏了已有别名")
			}
			batch["z.invalid"] = []Listener{listener}
			if err := register(batch); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.Trigger("a.alias", nil); err != nil || calls != 2 {
				t.Fatalf("失败后重试产生重复或遗漏回调: 次数=%d, 错误=%v", calls, err)
			}
			if err := dispatcher.DispatchProjectContext(context.Background(), NewEvent("order.created", nil)); err != nil {
				t.Fatal(err)
			}
			wantCalls := 4
			if application {
				wantCalls = 3
			}
			if calls != wantCalls {
				t.Fatalf("应用监听标记被改变: 次数=%d, 预期=%d", calls, wantCalls)
			}
		}
	}
}

// TestBatchListenerConcurrentTransactions 验证并发成功与失败批次不会覆盖其他注册或推进失败序号。
func TestBatchListenerConcurrentTransactions(t *testing.T) {
	const workers = 16
	dispatcher := NewDispatcher()
	listener := &SimpleListener{}
	var workersDone sync.WaitGroup
	for range workers {
		workersDone.Go(func() {
			if err := dispatcher.ListenEvents(map[string][]Listener{"a.ok": {listener}, "z.bad\x00": {listener}}); err == nil {
				t.Error("非法事件名应使批次失败")
			}
			if err := dispatcher.RegisterTransaction(func(staged *Dispatcher) error {
				return staged.ListenEvents(map[string][]Listener{"a.ok": {listener}, "b.*": {listener}})
			}); err != nil {
				t.Error(err)
			}
		})
	}
	workersDone.Wait()
	state := dispatcher.captureRegistrationState()
	if len(state.listeners["a.ok"]) != workers || len(state.listeners["b.*"]) != workers || state.sequence != workers*2 {
		t.Fatalf("并发批次发布不完整: 序号=%d, a=%d, b=%d", state.sequence, len(state.listeners["a.ok"]), len(state.listeners["b.*"]))
	}
}
