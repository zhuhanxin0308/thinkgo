package framework

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

// TestControllerStrictValidationPreservesThinkPHPArguments 验证严格开关不改变四参数签名和默认行为。
func TestControllerStrictValidationPreservesThinkPHPArguments(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	rules := map[string]string{"name|姓名": "required", "role": "in:member,admin"}
	validator := validate.NewValidator().SetRules(rules).SetScenes(map[string][]string{
		"create": {"name", "role"}, "update": {"name"},
	})
	if err := app.Instance(app.ParseClass("validate", "Accounts"), validator); err != nil {
		t.Fatal(err)
	}
	controller := &Controller{App: app}
	data := map[string]interface{}{"name": "运营", "extra": true}
	if !controller.Validate(data, rules) {
		t.Fatal("默认模式应保持兼容")
	}
	if controller.SetStrictValidate() != controller {
		t.Fatal("严格开关必须支持链式调用")
	}
	for _, definition := range []interface{}{rules, validator, "Accounts", "Accounts.update"} {
		actual := captureControllerValidationPanic(func() {
			controller.Validate(data, definition, map[string]string{"extra.unknown_field": "不允许额外字段"}, true)
		})
		failure, ok := actual.(*exception.ValidateException)
		if !ok || !reflect.DeepEqual(failure.GetError(), map[string]string{"extra": "不允许额外字段"}) {
			t.Fatalf("严格校验未保留批量与自定义消息: definition=%T failure=%#v", definition, actual)
		}
	}
	if result, err := controller.ValidateResult(data, rules); err != nil || result.Valid() || result.Violations()[0].Field != "extra" {
		t.Fatalf("显式结果未启用严格校验: result=%v err=%v", result.Errors(), err)
	}
	if !reflect.DeepEqual(data, map[string]interface{}{"name": "运营", "extra": true}) {
		t.Fatal("严格校验不得过滤或修改输入")
	}
	sceneData := map[string]interface{}{"name": "运营", "role": "member"}
	if !controller.Validate(sceneData, "Accounts.create") {
		t.Fatal("创建场景声明的字段应通过")
	}
	if actual := captureControllerValidationPanic(func() { controller.Validate(sceneData, "Accounts.update") }); actual == nil {
		t.Fatal("更新场景不得借用创建场景的字段白名单")
	}
	controller.SetStrictValidate(false)
	if !controller.Validate(data, "Accounts.update") {
		t.Fatal("关闭严格校验后应恢复兼容行为")
	}
	var nilController *Controller
	if nilController.SetStrictValidate() != nil {
		t.Fatal("空控制器链式配置应保持为空")
	}
}

// TestControllerValidationUsesRequestLanguage 验证规则、实例、命名场景与显式结果入口的请求语言一致。
func TestControllerValidationUsesRequestLanguage(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	for locale, content := range map[string]string{
		"zh-cn": `{"validate":{"default":{"required":"{:field}不能为空"}}}`,
		"en-us": `{"validate":{"default":{"required":"{:field} is required in English"}}}`,
	} {
		path := filepath.Join(t.TempDir(), locale+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := app.lang.Load(path, locale); err != nil {
			t.Fatal(err)
		}
	}
	rules := map[string]string{"name|姓名": "required"}
	validator := validate.NewValidator().SetRules(rules).AddScene("create", []string{"name"})
	if err := app.Instance(app.ParseClass("validate", "Accounts"), validator); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	const requests = 32
	for index := range requests {
		workers.Go(func() {
			locale, expected := "zh-cn", "姓名不能为空"
			if index%2 == 1 {
				locale, expected = "en-us", "姓名 is required in English"
			}
			request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
			request.Set(LangRequestKey, locale)
			controller := &Controller{App: app, Request: request}
			controller.SetStrictValidate(index%2 == 0)
			for _, definition := range []interface{}{rules, validator, "Accounts", "Accounts.create"} {
				actual := captureControllerValidationPanic(func() { controller.Validate(nil, definition) })
				failure, ok := actual.(*exception.ValidateException)
				if !ok || failure.GetError() != expected {
					t.Errorf("控制器请求语言错误: locale=%s actual=%#v", locale, actual)
				}
			}
			result, err := controller.ValidateResult(nil, rules)
			if err != nil || result.FirstError() != expected {
				t.Errorf("显式结果语言错误: locale=%s result=%v err=%v", locale, result.Errors(), err)
			}
			actual := captureControllerValidationPanic(func() {
				controller.Validate(map[string]interface{}{"name": "运营", "extra": true}, "Accounts.create")
			})
			if (actual != nil) != (index%2 == 0) {
				t.Errorf("严格开关污染其他请求: strict=%v actual=%v", index%2 == 0, actual)
			}
		})
	}
	workers.Wait()
	if app.lang.GetLang() != "zh-cn" {
		t.Fatal("请求验证修改了共享语言服务")
	}
	if result, err := validator.Validate(nil); err != nil || result.FirstError() != "姓名 is required" {
		t.Fatalf("控制器污染了共享验证器默认语言: result=%v err=%v", result.Errors(), err)
	}
}
