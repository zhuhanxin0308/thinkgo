package event

import (
	"errors"
	"reflect"
	"testing"
)

// TestDispatcherThinkPHPEventAPI 验证 bind、listen、trigger、hasListener、remove
// 的名称和默认执行顺序与 ThinkPHP 事件管理器一致。
func TestDispatcherThinkPHPEventAPI(t *testing.T) {
	dispatcher := NewDispatcher()
	if err := dispatcher.Bind(map[string]Factory{
		"OrderPaid": func(data interface{}) Event {
			return NewEvent("domain.OrderPaid", data)
		},
	}); err != nil {
		t.Fatalf("注册事件别名失败: %v", err)
	}

	order := make([]string, 0, 2)
	if err := dispatcher.Listen("OrderPaid", &SimpleListener{Handler: func(Event) error {
		order = append(order, "normal")
		return nil
	}}); err != nil {
		t.Fatalf("注册普通监听器失败: %v", err)
	}
	if err := dispatcher.Listen("OrderPaid", &SimpleListener{Handler: func(Event) error {
		order = append(order, "first")
		return nil
	}}, true); err != nil {
		t.Fatalf("注册首位监听器失败: %v", err)
	}
	if !dispatcher.HasListener("OrderPaid") {
		t.Fatal("事件别名应能查询到监听器")
	}
	if err := dispatcher.Trigger("OrderPaid", map[string]interface{}{"id": 8}); err != nil {
		t.Fatalf("触发事件别名失败: %v", err)
	}
	if want := []string{"first", "normal"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("监听顺序错误: want=%#v got=%#v", want, order)
	}
	if err := dispatcher.Remove("OrderPaid"); err != nil {
		t.Fatalf("移除事件监听器失败: %v", err)
	}
	if dispatcher.HasListener("OrderPaid") {
		t.Fatal("移除后不应再存在事件监听器")
	}
}

// TestDispatcherBindRejectsInvalidFactory 验证无效别名工厂不会留下部分绑定。
func TestDispatcherBindRejectsInvalidFactory(t *testing.T) {
	dispatcher := NewDispatcher()
	err := dispatcher.Bind(map[string]Factory{
		"Broken": func(interface{}) Event { panic("broken factory") },
	})
	if !errors.Is(err, ErrEventCallbackPanic) {
		t.Fatalf("工厂 panic 应转换为明确错误: %v", err)
	}
	if err := dispatcher.Trigger("Broken", nil); err != nil {
		t.Fatalf("失败绑定不得污染后续普通事件触发: %v", err)
	}
}
