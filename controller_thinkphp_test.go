package framework

import (
	"errors"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

// TestControllerOnlyExposesThinkPHPInitializeHook 验证基础控制器不会要求业务层
// 了解 App、Request 的底层注入入口，业务初始化只使用 Initialize。
func TestControllerOnlyExposesThinkPHPInitializeHook(t *testing.T) {
	typeOfController := reflect.TypeOf(&Controller{})
	if _, exists := typeOfController.MethodByName("Initialize"); !exists {
		t.Fatal("基础控制器必须提供 ThinkPHP initialize 对应的 Initialize 钩子")
	}
	if _, exists := typeOfController.MethodByName("Init"); exists {
		t.Fatal("基础控制器不得暴露偏离 ThinkPHP 业务契约的 Init 注入方法")
	}
}

// TestControllerValidateMatchesThinkPHPBaseController 验证 BaseController.Validate
// 对有效数据返回 true，对无效数据抛出 ValidateException，并支持消息和批量参数。
func TestControllerValidateMatchesThinkPHPBaseController(t *testing.T) {
	controller := &Controller{}
	rules := map[string]string{
		"email": "required|email",
		"name":  "required|min:2",
	}
	if !controller.Validate(map[string]interface{}{
		"email": "user@example.com",
		"name":  "ThinkPHP",
	}, rules) {
		t.Fatal("有效数据应返回 true")
	}

	assertValidatePanic := func(batch bool) *exception.ValidateException {
		t.Helper()
		var recovered interface{}
		func() {
			defer func() { recovered = recover() }()
			controller.Validate(
				map[string]interface{}{"email": "invalid", "name": ""},
				rules,
				map[string]string{
					"email.email":   "邮箱格式错误",
					"name.required": "名称不能为空",
					"name.min":      "名称不能为空",
				},
				batch,
			)
		}()
		validation, ok := recovered.(*exception.ValidateException)
		if !ok {
			t.Fatalf("无效数据应抛出 ValidateException，实际为 %T(%v)", recovered, recovered)
		}
		return validation
	}

	single := assertValidatePanic(false)
	if single.GetKey() != "email" || single.GetError() != "邮箱格式错误" {
		t.Fatalf("单条验证异常错误: key=%q error=%#v", single.GetKey(), single.GetError())
	}

	batch := assertValidatePanic(true)
	wantErrors := map[string]string{"email": "邮箱格式错误", "name": "名称不能为空"}
	if batch.GetKey() != "" || !reflect.DeepEqual(batch.GetError(), wantErrors) {
		t.Fatalf("批量验证异常错误: key=%q error=%#v", batch.GetKey(), batch.GetError())
	}
}

// TestControllerValidateResultKeepsExplicitResultAPI 验证需要自行组织响应的业务
// 可以显式使用 ValidateResult，而不会改变 ThinkPHP Validate 的默认异常语义。
func TestControllerValidateResultKeepsExplicitResultAPI(t *testing.T) {
	controller := &Controller{}
	result, err := controller.ValidateResult(map[string]interface{}{}, map[string]string{"name": "required"})
	if err != nil || result.Valid() || result.Violations()[0].Field != "name" {
		t.Fatalf("数据违规应通过 Result 返回: result=%#v err=%v", result, err)
	}

	result, err = controller.ValidateResult(map[string]interface{}{}, map[string]string{"name": "requried"})
	if !errors.Is(err, validate.ErrUnknownRule) || !result.Valid() {
		t.Fatalf("规则配置错误应通过 error 返回: result=%#v err=%v", result, err)
	}
}
