package event

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestDispatcherRegistrationCompatibilityAndTransactions 验证兼容注册入口、
// 应用级监听隔离以及事务提交保护共享同一套原子注册语义。
func TestDispatcherRegistrationCompatibilityAndTransactions(t *testing.T) {
	dispatcher := NewDispatcher()
	var projectCalls atomic.Int32
	var applicationCalls atomic.Int32
	if err := dispatcher.BindEvent("order.alias", func(data interface{}) Event {
		return NewEvent("order.created", data)
	}); err != nil {
		t.Fatalf("注册单个事件别名失败: %v", err)
	}
	if err := dispatcher.ListenEvents(map[string][]Listener{
		"order.created": {
			&SimpleListener{Handler: func(Event) error {
				projectCalls.Add(1)
				return nil
			}},
		},
	}); err != nil {
		t.Fatalf("批量注册项目监听器失败: %v", err)
	}
	if err := dispatcher.ListenApplicationEvents(map[string][]Listener{
		"order.created": {
			&SimpleListener{Handler: func(Event) error {
				applicationCalls.Add(1)
				return nil
			}},
		},
	}); err != nil {
		t.Fatalf("批量注册应用监听器失败: %v", err)
	}
	if err := dispatcher.Trigger("order.alias", map[string]string{"id": "order-1"}); err != nil {
		t.Fatalf("通过事件别名触发失败: %v", err)
	}
	if projectCalls.Load() != 1 || applicationCalls.Load() != 1 {
		t.Fatalf("普通分发应包含项目和应用监听器: project=%d application=%d", projectCalls.Load(), applicationCalls.Load())
	}
	//lint:ignore SA1012 此处专门验证空上下文被拒绝，不能用有效上下文替代错误输入。
	if err := dispatcher.DispatchProjectContext(nil, NewEvent("order.created", nil)); !errors.Is(err, ErrInvalidEventContext) {
		t.Fatalf("项目事件分发收到 nil 上下文时应失败: %v", err)
	}
	if err := dispatcher.DispatchProjectContext(testEventContext(), NewEvent("order.created", nil)); err != nil {
		t.Fatalf("项目事件分发失败: %v", err)
	}
	if projectCalls.Load() != 2 || applicationCalls.Load() != 1 {
		t.Fatalf("项目事件分发不应触发应用监听器: project=%d application=%d", projectCalls.Load(), applicationCalls.Load())
	}

	if err := dispatcher.RegisterTransaction(func(staged *Dispatcher) error {
		if err := staged.BindEvent("payment.alias", func(data interface{}) Event {
			return NewEvent("payment.created", data)
		}); err != nil {
			return err
		}
		return staged.Listen("payment.created", &SimpleListener{Handler: func(Event) error { return nil }})
	}); err != nil {
		t.Fatalf("成功注册事件事务失败: %v", err)
	}
	if !dispatcher.HasListener("payment.created") {
		t.Fatal("成功注册事件事务未发布监听器")
	}
	rollbackErr := errors.New("transaction rollback")
	if err := dispatcher.RegisterTransaction(func(staged *Dispatcher) error {
		if listenErr := staged.Listen("rollback.event", &SimpleListener{}); listenErr != nil {
			return listenErr
		}
		return rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("失败事件事务应返回回调错误: %v", err)
	}
	if dispatcher.HasListener("rollback.event") {
		t.Fatal("失败事件事务不应发布暂存监听器")
	}

	var guardReleased atomic.Bool
	if err := dispatcher.RegisterTransactionWithCommitGuard(
		func(staged *Dispatcher) error {
			return staged.Listen("guard.event", &SimpleListener{})
		},
		func() (func(), error) {
			return func() { guardReleased.Store(true) }, nil
		},
	); err != nil {
		t.Fatalf("带提交保护的事件事务失败: %v", err)
	}
	if !guardReleased.Load() || !dispatcher.HasListener("guard.event") {
		t.Fatal("提交保护应在事务发布后释放且保留监听器")
	}
	guardErr := errors.New("commit guard rejected")
	if err := dispatcher.RegisterTransactionWithCommitGuard(
		func(staged *Dispatcher) error {
			return staged.Listen("guard.rejected", &SimpleListener{})
		},
		func() (func(), error) { return nil, guardErr },
	); !errors.Is(err, guardErr) {
		t.Fatalf("提交保护拒绝时应返回保护错误: %v", err)
	}
	if dispatcher.HasListener("guard.rejected") {
		t.Fatal("提交保护拒绝时不应发布暂存监听器")
	}
	if err := dispatcher.RegisterTransactionWithCommitGuard(nil, nil); !errors.Is(err, ErrInvalidSubscriber) {
		t.Fatalf("缺少提交保护时应返回订阅者错误: %v", err)
	}
}

// testEventContext 返回项目事件分发所需的最小有效上下文。
func testEventContext() context.Context {
	return context.Background()
}
