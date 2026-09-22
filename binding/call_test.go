package binding

import (
	"errors"
	"reflect"
	"testing"
)

type callService interface{ Name() string }

// TestCompileCallClassification 验证真实业务接口、动态请求值和请求标记不会混淆。
func TestCompileCallClassification(t *testing.T) {
	plan, err := CompileCall(reflect.TypeOf(func(callService, any, markedInput, ...int) (string, error) { return "", nil }), false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Variadic || !plan.ReturnsError || plan.ErrorOnly || plan.Input != reflect.TypeFor[markedInput]() || plan.Output != reflect.TypeFor[string]() {
		t.Fatalf("签名计划错误: %#v", plan)
	}
	for index, kind := range []ArgumentKind{ArgumentDependency, ArgumentValue, ArgumentInput, ArgumentValue} {
		if plan.Arguments[index].Kind != kind {
			t.Fatalf("参数 %d 分类错误: %#v", index, plan.Arguments[index])
		}
	}
	method, err := CompileCall(reflect.TypeOf(func(*int, callService) error { return nil }), true)
	if err != nil || len(method.Arguments) != 1 || !method.ErrorOnly || method.Output != nil {
		t.Fatalf("接收者或错误出口不正确: %#v %v", method, err)
	}
	if IsValueType(nil) || IsValueType(reflect.TypeFor[*callService]()) || !IsValueType(reflect.TypeFor[[]*int]()) {
		t.Fatal("复合类型分类错误")
	}
}

// TestCompileCallRejectsInvalidContracts 验证启动期拒绝不能执行的处理器定义。
func TestCompileCallRejectsInvalidContracts(t *testing.T) {
	tooMany := make([]reflect.Type, MaximumCallParameters+1)
	for index := range tooMany {
		tooMany[index] = reflect.TypeFor[int]()
	}
	for _, signature := range []reflect.Type{
		nil, reflect.TypeFor[int](), reflect.FuncOf(tooMany, nil, false),
		reflect.TypeOf(func(markedInput, *markedInput) {}),
		reflect.TypeOf(func(invalidMarkedInput) {}),
		reflect.TypeOf(func(...markedInput) {}),
		reflect.TypeOf(func(...callService) {}),
		reflect.TypeOf(func() (int, int) { return 0, 0 }),
		reflect.TypeOf(func() (int, int, error) { return 0, 0, nil }),
	} {
		if _, err := CompileCall(signature, false); !errors.Is(err, ErrDefinition) {
			t.Fatalf("非法定义未拒绝: %v %v", signature, err)
		}
	}
	if _, err := CompileCall(reflect.TypeOf(func() {}), true); !errors.Is(err, ErrDefinition) {
		t.Fatalf("缺失接收者未拒绝: %v", err)
	}
}
