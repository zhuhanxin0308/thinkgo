package event

import (
	"errors"
	"testing"
	"time"
)

type partialRegistrationSubscriber struct {
	listener Listener
	err      error
	panicNow bool
	entered  chan<- struct{}
}

func (s *partialRegistrationSubscriber) Subscribe(dispatcher *Dispatcher) error {
	if s.entered != nil {
		close(s.entered)
	}
	if err := dispatcher.Listen("partial.first", s.listener); err != nil {
		return err
	}
	if err := dispatcher.Listen("partial.*", s.listener); err != nil {
		return err
	}
	if s.panicNow {
		panic("partial subscriber panic")
	}
	return s.err
}

type nestedRegistrationSubscriber struct {
	listener Listener
}

func (s *nestedRegistrationSubscriber) Subscribe(dispatcher *Dispatcher) error {
	if err := dispatcher.Listen("nested.outer", s.listener); err != nil {
		return err
	}
	return dispatcher.Subscribe(&partialRegistrationSubscriber{listener: s.listener})
}

type blockingRegistrationSubscriber struct {
	started  chan<- struct{}
	release  <-chan struct{}
	err      error
	listener Listener
}

func (s *blockingRegistrationSubscriber) Subscribe(dispatcher *Dispatcher) error {
	listener := s.listener
	if listener == nil {
		listener = &SimpleListener{Handler: func(Event) error { return nil }}
	}
	if err := dispatcher.Listen("concurrent.partial", listener); err != nil {
		return err
	}
	close(s.started)
	<-s.release
	return s.err
}

// TestDispatcherSubscribeRollsBackPartialRegistration 验证订阅回调失败时，已注册的部分监听器会完整回滚。
func TestDispatcherSubscribeRollsBackPartialRegistration(t *testing.T) {
	dispatcher := NewDispatcher()
	baseCalled := 0
	if err := dispatcher.Listen("existing", &SimpleListener{Handler: func(Event) error {
		baseCalled++
		return nil
	}}); err != nil {
		t.Fatalf("注册已有监听器失败: %v", err)
	}

	listenerCalled := 0
	listener := &SimpleListener{Handler: func(Event) error {
		listenerCalled++
		return nil
	}}
	subscribeErr := errors.New("订阅配置无效")
	if err := dispatcher.Subscribe(&partialRegistrationSubscriber{listener: listener, err: subscribeErr}); !errors.Is(err, subscribeErr) {
		t.Fatalf("订阅失败应返回原始错误，实际为 %v", err)
	}
	if dispatcher.HasListeners("partial.first") || dispatcher.HasListeners("partial.*") {
		t.Fatal("订阅失败后不应残留任何部分监听器")
	}
	if !dispatcher.HasListeners("existing") {
		t.Fatal("订阅失败不应删除事务开始前的监听器")
	}

	if err := dispatcher.Dispatch(NewEvent("existing", nil)); err != nil {
		t.Fatalf("派发已有事件失败: %v", err)
	}
	if baseCalled != 1 || listenerCalled != 0 {
		t.Fatalf("回滚后的监听器状态不正确，已有监听器调用 %d 次，部分监听器调用 %d 次", baseCalled, listenerCalled)
	}

	if err := dispatcher.Subscribe(&partialRegistrationSubscriber{listener: listener}); err != nil {
		t.Fatalf("重试成功订阅失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("partial.first", nil)); err != nil {
		t.Fatalf("派发重试后的事件失败: %v", err)
	}
	if listenerCalled != 2 {
		t.Fatalf("重试成功后应只保留一组精确与通配监听器，实际调用 %d 次", listenerCalled)
	}
}

// TestDispatcherSubscribeRollsBackWhenSubscriberPanics 验证订阅回调 panic 也不会留下部分注册结果。
func TestDispatcherSubscribeRollsBackWhenSubscriberPanics(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &SimpleListener{Handler: func(Event) error { return nil }}

	if err := dispatcher.Subscribe(&partialRegistrationSubscriber{listener: listener, panicNow: true}); !errors.Is(err, ErrEventCallbackPanic) {
		t.Fatalf("订阅 panic 应转换为 ErrEventCallbackPanic，实际为 %v", err)
	}
	if dispatcher.HasListeners("partial.first") || dispatcher.HasListeners("partial.*") {
		t.Fatal("订阅 panic 后不应残留任何部分监听器")
	}
}

// TestDispatcherSubscribeSupportsNestedRegistration 验证订阅回调再次订阅时不会死锁，且会合并到同一事务。
func TestDispatcherSubscribeSupportsNestedRegistration(t *testing.T) {
	dispatcher := NewDispatcher()
	called := 0
	listener := &SimpleListener{Handler: func(Event) error {
		called++
		return nil
	}}
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Subscribe(&nestedRegistrationSubscriber{listener: listener})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("嵌套订阅失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("嵌套订阅发生死锁")
	}

	if err := dispatcher.Dispatch(NewEvent("nested.outer", nil)); err != nil {
		t.Fatalf("派发嵌套订阅的精确事件失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("partial.first", nil)); err != nil {
		t.Fatalf("派发嵌套订阅的通配事件失败: %v", err)
	}
	if called != 3 {
		t.Fatalf("嵌套订阅应提交一组精确和通配监听器，实际调用 %d 次", called)
	}
}

// TestDispatcherSubscriptionChangesRemainInvisibleUntilCommit 验证订阅回调尚未成功返回时，
// 其他协程既不能观察到部分监听器，也不能提前派发它们。
func TestDispatcherSubscriptionChangesRemainInvisibleUntilCommit(t *testing.T) {
	dispatcher := NewDispatcher()
	started := make(chan struct{})
	release := make(chan struct{})
	called := 0
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Subscribe(&blockingRegistrationSubscriber{
			started: started,
			release: release,
			listener: &SimpleListener{Handler: func(Event) error {
				called++
				return nil
			}},
		})
	}()
	<-started

	visibleBeforeCommit := dispatcher.HasListeners("concurrent.partial")
	if err := dispatcher.Dispatch(NewEvent("concurrent.partial", nil)); err != nil {
		close(release)
		<-done
		t.Fatalf("订阅提交前派发事件失败: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("提交订阅失败: %v", err)
	}
	if visibleBeforeCommit {
		t.Fatal("订阅提交前不应暴露部分监听器")
	}
	if called != 0 {
		t.Fatalf("订阅提交前不应调用暂存监听器，实际为 %d", called)
	}
	if !dispatcher.HasListeners("concurrent.partial") {
		t.Fatal("订阅成功后应原子发布监听器")
	}
}

// TestDispatcherConcurrentSubscriptionsAreIndependent 验证互不相关的并发订阅拥有独立事务，
// 一个订阅失败不能回滚另一个已经成功的订阅。
func TestDispatcherConcurrentSubscriptionsAreIndependent(t *testing.T) {
	dispatcher := NewDispatcher()
	started := make(chan struct{})
	release := make(chan struct{})
	failed := errors.New("并发订阅失败")
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- dispatcher.Subscribe(&blockingRegistrationSubscriber{started: started, release: release, err: failed})
	}()
	<-started

	secondDone := make(chan error, 1)
	secondEntered := make(chan struct{})
	go func() {
		secondDone <- dispatcher.Subscribe(&partialRegistrationSubscriber{
			listener: &SimpleListener{Handler: func(Event) error { return nil }},
			entered:  secondEntered,
		})
	}()
	// 旧实现会让第二个回调直接进入共享事务；隔离实现会等第一个事务结束后再执行。
	select {
	case <-secondEntered:
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	if err := <-firstDone; !errors.Is(err, failed) {
		t.Fatalf("失败订阅应返回原始错误，实际为 %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("并发成功订阅不应报告错误，实际为 %v", err)
	}
	if dispatcher.HasListeners("concurrent.partial") {
		t.Fatal("失败订阅的监听器不应残留")
	}
	if !dispatcher.HasListeners("partial.first") || !dispatcher.HasListeners("partial.other") {
		t.Fatal("成功的并发订阅不应被另一事务回滚")
	}
}
