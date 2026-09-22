package framework

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	frameworkContext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

// TestBaseControllerThinkPHPBusinessAPI 验证控制器中间件、视图赋值、标准
// Result/Success/Error、Redirect、请求缓存和请求语言等业务入口。
func TestBaseControllerThinkPHPBusinessAPI(t *testing.T) {
	basePath := t.TempDir()
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	app.cache = cache.NewCache(nil, cacheDriver.NewMemory())
	translationFile := filepath.Join(basePath, "zh-cn.json")
	if err := os.WriteFile(translationFile, []byte(`{"welcome":"你好，{:name}"}`), 0o600); err != nil {
		t.Fatalf("写入控制器语言包失败: %v", err)
	}
	if err := app.lang.Load(translationFile, "zh-cn"); err != nil {
		t.Fatalf("加载控制器语言包失败: %v", err)
	}
	request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	request.Set(LangRequestKey, "zh-cn")
	controller := &Controller{App: app, Request: request}

	controller.Initialize()
	middlewares := []ControllerMiddleware{
		{Name: "auth", Only: []string{"index"}},
		{Name: "trace", Except: []string{"health"}},
	}
	controller.SetMiddleware(middlewares...)
	if !reflect.DeepEqual(controller.GetMiddleware(), middlewares) {
		t.Fatalf("控制器中间件声明错误: %#v", controller.GetMiddleware())
	}
	if controller.SetBatchValidate() != controller || !controller.batchValidate {
		t.Fatal("SetBatchValidate() 应开启批量验证并支持链式调用")
	}
	if controller.SetBatchValidate(false) != controller || controller.batchValidate {
		t.Fatal("SetBatchValidate(false) 应关闭批量验证")
	}
	controller.Assign("title", "ThinkPHP")
	controller.Assign("count", 2)
	if !reflect.DeepEqual(controller.viewData, map[string]interface{}{"title": "ThinkPHP", "count": 2}) {
		t.Fatalf("Assign 视图变量错误: %#v", controller.viewData)
	}

	assertControllerJSON := func(response *Response, expectedCode int, expectedMessage string, expectedData interface{}) {
		t.Helper()
		var payload map[string]interface{}
		if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
			t.Fatalf("解析控制器 JSON 响应失败: %v", err)
		}
		if int(payload["code"].(float64)) != expectedCode || payload["msg"] != expectedMessage || !reflect.DeepEqual(payload["data"], expectedData) {
			t.Fatalf("控制器 JSON 响应错误: %#v", payload)
		}
	}
	assertControllerJSON(controller.Success(map[string]interface{}{"id": float64(7)}), 0, "success", map[string]interface{}{"id": float64(7)})
	assertControllerJSON(controller.Success("created", "保存成功"), 0, "保存成功", "created")
	assertControllerJSON(controller.Error("保存失败"), 1, "保存失败", nil)
	assertControllerJSON(controller.Error("权限不足", 40301), 40301, "权限不足", nil)
	assertControllerJSON(controller.Result(true, 12, "custom"), 12, "custom", true)

	redirect := controller.Redirect("/login")
	if redirect.GetStatus() != http.StatusFound || redirect.GetHeader("Location") != "/login" {
		t.Fatalf("控制器默认重定向错误: status=%d location=%q", redirect.GetStatus(), redirect.GetHeader("Location"))
	}
	redirect = controller.Redirect("/moved", http.StatusMovedPermanently)
	if redirect.GetStatus() != http.StatusMovedPermanently {
		t.Fatalf("控制器显式重定向状态错误: %d", redirect.GetStatus())
	}

	requestCache := controller.RequestCache()
	if requestCache == nil {
		t.Fatal("RequestCache 不应要求业务代码自行绑定调试 collector")
	}
	if err := requestCache.Set("controller", "cache", time.Minute); err != nil {
		t.Fatalf("控制器请求缓存写入失败: %v", err)
	}
	if value, found, err := app.cache.Get("controller"); err != nil || !found || value != "cache" {
		t.Fatalf("请求缓存没有共享应用 store: value=%#v found=%t err=%v", value, found, err)
	}
	if translated := controller.Lang("welcome", map[string]interface{}{"name": "ThinkGo"}); translated != "你好，ThinkGo" {
		t.Fatalf("控制器 Lang 翻译错误: %q", translated)
	}
	if controller.GetLang() != "zh-cn" {
		t.Fatalf("控制器请求语言错误: %q", controller.GetLang())
	}

	if (*Controller)(nil).SetBatchValidate() != nil || (*Controller)(nil).RequestCache() != nil {
		t.Fatal("nil Controller 链式配置与请求缓存必须安全返回 nil")
	}
	if (&Controller{}).RequestCache() != nil || (&Controller{App: &App{}}).RequestCache() != nil {
		t.Fatal("缺少 App 或 Cache 时 RequestCache 必须返回 nil")
	}
	if (&Controller{}).GetLang() != "" {
		t.Fatal("缺少 Request 时 GetLang 必须返回空语言")
	}
}

// TestBaseControllerNamedValidatorAPI 验证业务可直接使用 Validator.scene 名称，
// 容器解析、场景选择、自定义消息和异常生命周期无需接触底层验证器装配。
func TestBaseControllerNamedValidatorAPI(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	validator := validate.NewValidator().
		SetRules(map[string]string{"email": "required|email", "name": "required|min:2"}).
		SetScenes(map[string][]string{"create": {"email", "name"}, "email": {"email"}})
	serviceName := app.ParseClass("validate", "User")
	if err := app.Instance(serviceName, validator); err != nil {
		t.Fatalf("注册命名验证器失败: %v", err)
	}
	controller := &Controller{App: app}
	if !controller.Validate(map[string]interface{}{"email": "user@example.com", "name": "ThinkGo"}, "User.create") {
		t.Fatal("命名场景验证有效数据应返回 true")
	}

	recovered := captureControllerValidationPanic(func() {
		controller.Validate(
			map[string]interface{}{"email": "invalid"},
			"User.email",
			map[string]string{"email.email": "邮箱格式错误"},
		)
	})
	validation, ok := recovered.(*exception.ValidateException)
	if !ok || validation.GetKey() != "email" || validation.GetError() != "邮箱格式错误" {
		t.Fatalf("命名验证器异常错误: %#v", recovered)
	}

	invalidCalls := []func(){
		func() { controller.Validate(nil, 7) },
		func() { controller.Validate(nil, map[string]string{}, "message") },
		func() { controller.Validate(nil, map[string]string{}, nil, "batch") },
		func() { controller.Validate(nil, map[string]string{}, nil, false, "extra") },
		func() { (&Controller{}).Validate(nil, "User") },
		func() { controller.Validate(nil, "") },
		func() {
			_ = app.Instance(app.ParseClass("validate", "Wrong"), "not-validator")
			controller.Validate(nil, "Wrong")
		},
		func() { controller.Validate(nil, validationContractStub{}, map[string]string{"field": "message"}) },
	}
	for index, call := range invalidCalls {
		if recovered = captureControllerValidationPanic(call); recovered == nil {
			t.Errorf("第 %d 个非法 Validate 调用必须触发 panic", index+1)
		}
	}

	result, err := controller.ValidateResult(map[string]interface{}{"date": "2026-08-26"}, map[string]string{"date": "date"})
	if err != nil || !result.Valid() {
		t.Fatalf("带 App 时 ValidateResult 应使用应用时区: result=%#v err=%v", result, err)
	}
}

type validationContractStub struct{}

func (validationContractStub) Validate(map[string]interface{}, ...validate.Option) (validate.Result, error) {
	return validate.Result{}, nil
}

func captureControllerValidationPanic(call func()) (recovered interface{}) {
	defer func() { recovered = recover() }()
	call()
	return nil
}

// TestNewResponseTopLevelFacade 验证业务控制器无需导入 context 子包即可创建响应。
func TestNewResponseTopLevelFacade(t *testing.T) {
	response := NewResponse().Content("ThinkPHP")
	if response.GetStatus() != http.StatusOK || response.GetContent() != "ThinkPHP" || response.Error() != nil {
		t.Fatalf("顶层 NewResponse 错误: status=%d body=%q err=%v", response.GetStatus(), response.GetContent(), response.Error())
	}
	if !errors.Is(NewResponse().Redirect("", http.StatusFound).Error(), frameworkContext.ErrInvalidRedirect) {
		t.Fatal("顶层 Response 必须保留 context 响应错误类型")
	}
}
