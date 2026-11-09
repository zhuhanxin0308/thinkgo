package validate

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// TestRequiredRejectsCyclicValues 用独立测试进程隔离潜在死循环，失败时不会遗留忙等协程。
func TestRequiredRejectsCyclicValues(t *testing.T) {
	const childFlag = "THINKGO_REQUIRED_CYCLE_CHILD"
	if os.Getenv(childFlag) != "1" {
		const timeout = 5 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRequiredRejectsCyclicValues$")
		command.Env = append(os.Environ(), childFlag+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("循环引用验证未在期限内正常拒绝: %v，超时=%v，输出=%s", err, ctx.Err(), output)
		}
		return
	}
	var self, left, right any
	self = &self
	left, right = &right, &left
	for _, value := range []any{self, left} {
		for _, rule := range []string{"required", "requireIf:trigger,yes", "requireWith:trigger"} {
			validator := NewValidator().SetRules(map[string]string{"value": rule})
			result, err := validator.Validate(map[string]any{"value": value, "trigger": "yes"})
			if err != nil || result.Valid() {
				t.Fatalf("规则 %q 未拒绝循环值: 错误=%v", rule, err)
			}
		}
	}
}

// TestRequiredIndirectionBoundaries 保持正常解引用、空值和条件规则语义，并拒绝过深输入。
func TestRequiredIndirectionBoundaries(t *testing.T) {
	text, blank := "内容", " \t"
	var nilPointer *string
	for _, testCase := range []struct {
		name  string
		value any
		valid bool
	}{
		{"非空指针", &text, true}, {"空字符串指针", &blank, false},
		{"空指针", nilPointer, false}, {"空接口", nil, false},
		{"空集合", []string{}, false}, {"非空集合", []string{"值"}, true},
		{"空映射", map[string]int{}, false}, {"非空映射", map[string]int{"值": 1}, true},
		{"零值数值", 0, true}, {"假值", false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, rule := range []string{"required", "requireIf:trigger,yes", "requireWith:trigger"} {
				result, err := NewValidator().SetRules(map[string]string{"value": rule}).Validate(map[string]any{"value": testCase.value, "trigger": "yes"})
				if err != nil || result.Valid() != testCase.valid {
					t.Fatalf("规则 %q 的结果不符: 预期=%t，错误=%v", rule, testCase.valid, err)
				}
			}
		})
	}
	// 已有标量规则最多检查 32 层反射值；第 32 层仍为指针时拒绝。
	const reflectionDepthLimit = 32
	value := reflect.ValueOf(text)
	for depth := 1; depth <= reflectionDepthLimit+1; depth++ {
		pointer := reflect.New(value.Type())
		pointer.Elem().Set(value)
		value = pointer
		result, err := NewValidator().SetRules(map[string]string{"value": "required"}).Validate(map[string]any{"value": value.Interface()})
		if err != nil || result.Valid() != (depth < reflectionDepthLimit) {
			t.Fatalf("解引用深度 %d 的判断不符，错误=%v", depth, err)
		}
	}
}
