package framework

import (
	"errors"
	"testing"

	"thinkgo/framework/validate"
)

// TestControllerValidateSeparatesDataAndConfigurationErrors 验证控制器便捷方法保留新验证 API 的错误边界。
func TestControllerValidateSeparatesDataAndConfigurationErrors(t *testing.T) {
	controller := &Controller{}
	result, err := controller.Validate(map[string]interface{}{}, map[string]string{"name": "required"})
	if err != nil || result.Valid() || result.Violations()[0].Field != "name" {
		t.Fatalf("数据违规应通过 Result 返回: result=%#v err=%v", result, err)
	}

	result, err = controller.Validate(map[string]interface{}{}, map[string]string{"name": "requried"})
	if !errors.Is(err, validate.ErrUnknownRule) || !result.Valid() {
		t.Fatalf("规则配置错误应通过 error 返回: result=%#v err=%v", result, err)
	}
}
