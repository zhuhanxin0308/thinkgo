package event

import "testing"

type listenerHandleSubscriber struct {
	handle **ListenerHandle
	calls  *int
}

func (subscriber *listenerHandleSubscriber) Subscribe(dispatcher *Dispatcher) error {
	handle, err := dispatcher.ListenWithHandle("plugin.transaction", &SimpleListener{Handler: func(Event) error {
		*subscriber.calls++
		return nil
	}})
	if err != nil {
		return err
	}
	*subscriber.handle = handle
	return nil
}

// TestListenerHandleRemovesOnlyOwnedListener 验证监听句柄只撤销自己注册的监听器，
// 不会破坏同一事件上由其它扩展注册的监听器。
func TestListenerHandleRemovesOnlyOwnedListener(t *testing.T) {
	dispatcher := NewDispatcher()
	firstCalls := 0
	secondCalls := 0

	handle, err := dispatcher.ListenWithHandle("plugin.shared", &SimpleListener{Handler: func(Event) error {
		firstCalls++
		return nil
	}})
	if err != nil {
		t.Fatalf("注册带句柄监听器失败: %v", err)
	}
	if handle == nil {
		t.Fatal("成功注册后必须返回监听句柄")
	}
	if err = dispatcher.Listen("plugin.shared", &SimpleListener{Handler: func(Event) error {
		secondCalls++
		return nil
	}}); err != nil {
		t.Fatalf("注册共享事件的第二个监听器失败: %v", err)
	}

	if err = dispatcher.Dispatch(NewEvent("plugin.shared", nil)); err != nil {
		t.Fatalf("首次分发共享事件失败: %v", err)
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("首次分发应调用两个监听器，first=%d second=%d", firstCalls, secondCalls)
	}

	if err = handle.Remove(); err != nil {
		t.Fatalf("按句柄移除监听器失败: %v", err)
	}
	if err = handle.Remove(); err != nil {
		t.Fatalf("监听句柄重复移除必须保持幂等: %v", err)
	}
	if err = dispatcher.Dispatch(NewEvent("plugin.shared", nil)); err != nil {
		t.Fatalf("移除后分发共享事件失败: %v", err)
	}
	if firstCalls != 1 || secondCalls != 2 {
		t.Fatalf("移除句柄后只能保留第二个监听器，first=%d second=%d", firstCalls, secondCalls)
	}
}

// TestListenerHandleSupportsWildcardAndPriority 验证通配监听和数值优先级也能
// 获得独立所有权句柄，避免动态插件回退到清空整个事件模式。
func TestListenerHandleSupportsWildcardAndPriority(t *testing.T) {
	dispatcher := NewDispatcher()
	calls := 0
	handle, err := dispatcher.ListenPriorityWithHandle("plugin.*", &SimpleListener{Handler: func(Event) error {
		calls++
		return nil
	}}, 10)
	if err != nil {
		t.Fatalf("注册带优先级通配监听器失败: %v", err)
	}
	if err = dispatcher.Dispatch(NewEvent("plugin.created", nil)); err != nil {
		t.Fatalf("分发通配事件失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("通配监听器应执行一次，实际为 %d", calls)
	}
	if err = handle.Remove(); err != nil {
		t.Fatalf("移除通配监听器失败: %v", err)
	}
	if dispatcher.HasListener("plugin.created") {
		t.Fatal("移除唯一通配监听器后不应继续报告匹配")
	}
}

// TestListenerHandleFollowsSubscriptionCommit 验证订阅事务提交后会把句柄迁移到
// 正式调度器；插件保存订阅阶段取得的句柄后，仍能精确卸载已发布监听器。
func TestListenerHandleFollowsSubscriptionCommit(t *testing.T) {
	dispatcher := NewDispatcher()
	var handle *ListenerHandle
	calls := 0
	if err := dispatcher.Subscribe(&listenerHandleSubscriber{handle: &handle, calls: &calls}); err != nil {
		t.Fatalf("注册带所有权句柄的订阅者失败: %v", err)
	}
	if handle == nil {
		t.Fatal("订阅者必须保留事务内创建的监听句柄")
	}
	if err := dispatcher.Dispatch(NewEvent("plugin.transaction", nil)); err != nil {
		t.Fatalf("分发事务提交后的事件失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("事务提交后的监听器应执行一次，实际为 %d", calls)
	}
	if err := handle.Remove(); err != nil {
		t.Fatalf("用订阅事务返回的句柄卸载监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("plugin.transaction", nil)); err != nil {
		t.Fatalf("卸载后再次分发事件失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("句柄卸载后监听器不得再次执行，实际为 %d", calls)
	}
}
