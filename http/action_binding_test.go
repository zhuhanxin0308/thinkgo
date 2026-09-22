package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

type thinkPHPActionBindingController struct{}

type thinkPHPConfiguredController struct{}

func (*thinkPHPConfiguredController) IndexAction() string {
	return "configured"
}

func (*thinkPHPActionBindingController) Show(application *framework.App, request *fwcontext.Request, name string, age int, enabled bool) map[string]interface{} {
	return map[string]interface{}{
		"app":     application != nil,
		"request": request != nil,
		"name":    name,
		"age":     age,
		"enabled": enabled,
	}
}

func (*thinkPHPActionBindingController) Optional(names ...string) string {
	if len(names) == 0 {
		return "ThinkPHP8"
	}
	return names[0]
}

// TestControllerActionBindsRouteParametersByDeclarationOrder 验证控制器动作可以像
// ThinkPHP 一样直接声明路由参数，同时仍可声明 App 和 Request 依赖。
func TestControllerActionBindsRouteParametersByDeclarationOrder(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	application.BindFactory("Binding", func() interface{} { return &thinkPHPActionBindingController{} })
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	if err := router.SetCompleteMatch(true); err != nil {
		t.Fatalf("启用完整路由匹配失败: %v", err)
	}
	if _, err := router.Get("/hello/:name/:age/:enabled", "binding/show"); err != nil {
		t.Fatalf("注册参数绑定路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/hello/ThinkGo/8/true", nil))
	matched, parameters, err := router.Match(request)
	if err != nil || matched == nil {
		t.Fatalf("匹配参数绑定路由失败: route=%#v err=%v", matched, err)
	}
	for name, value := range parameters {
		request.SetRoute(name, value)
	}
	response := handler.dispatch(matched, request)
	if response.GetStatus() != http.StatusOK {
		t.Fatalf("控制器参数绑定应成功，实际状态为 %d，响应为 %q", response.GetStatus(), string(response.GetBody()))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
		t.Fatalf("解析参数绑定响应失败: %v", err)
	}
	if payload["name"] != "ThinkGo" || payload["age"] != float64(8) || payload["enabled"] != true || payload["app"] != true || payload["request"] != true {
		t.Fatalf("控制器参数绑定结果错误: %#v", payload)
	}
}

// TestControllerActionSupportsOptionalVariadicParameter 验证 Go 的可变参数承担
// ThinkPHP 动作默认参数语义：路由变量缺失时由动作自身提供默认值。
func TestControllerActionSupportsOptionalVariadicParameter(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	application.BindFactory("Binding", func() interface{} { return &thinkPHPActionBindingController{} })
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	if err := router.SetCompleteMatch(true); err != nil {
		t.Fatalf("启用完整路由匹配失败: %v", err)
	}
	if _, err := router.Get("/optional/:name?", "binding/optional"); err != nil {
		t.Fatalf("注册可选参数路由失败: %v", err)
	}
	for _, testCase := range []struct {
		path string
		want string
	}{
		{path: "/optional", want: "ThinkPHP8"},
		{path: "/optional/ThinkGo", want: "ThinkGo"},
	} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+testCase.path, nil))
		matched, parameters, err := router.Match(request)
		if err != nil || matched == nil {
			t.Fatalf("匹配可选参数路由失败: path=%s route=%#v err=%v", testCase.path, matched, err)
		}
		for name, value := range parameters {
			request.SetRoute(name, value)
		}
		response := handler.dispatch(matched, request)
		if response.GetStatus() != http.StatusOK || string(response.GetBody()) != testCase.want {
			t.Fatalf("可选参数绑定错误: path=%s handler=%#v names=%#v params=%#v status=%d body=%q", testCase.path, matched.Handler(), matched.ParameterNames(), parameters, response.GetStatus(), string(response.GetBody()))
		}
	}
}

// TestControllerActionRejectsInvalidScalarConversion 验证非法路由参数不会以零值
// 静默进入业务逻辑。
func TestControllerActionRejectsInvalidScalarConversion(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	application.BindFactory("Binding", func() interface{} { return &thinkPHPActionBindingController{} })
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	registered, err := router.Get("/hello/:name/:age/:enabled", "binding/show")
	if err != nil {
		t.Fatalf("注册非法参数测试路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/hello/ThinkGo/not-an-int/true", nil))
	request.SetRoute("name", "ThinkGo")
	request.SetRoute("age", "not-an-int")
	request.SetRoute("enabled", "true")
	response := handler.dispatch(registered, request)
	if response.GetStatus() != http.StatusBadRequest {
		t.Fatalf("非法标量参数必须终止动作调用，实际状态为 %d", response.GetStatus())
	}
}

// TestControllerDispatchHonorsThinkPHPSuffixConfiguration 验证控制器和动作后缀
// 与 ThinkPHP 一样由 route 配置统一应用，路由定义仍只写业务名称。
func TestControllerDispatchHonorsThinkPHPSuffixConfiguration(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := application.Config().Set("route.controller_suffix", true); err != nil {
		t.Fatalf("设置控制器后缀失败: %v", err)
	}
	if err := application.Config().Set("route.action_suffix", "Action"); err != nil {
		t.Fatalf("设置动作后缀失败: %v", err)
	}
	application.BindFactory("ConfiguredController", func() interface{} { return &thinkPHPConfiguredController{} })
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	registered, err := router.Get("/configured", "configured/index")
	if err != nil {
		t.Fatalf("注册后缀配置路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/configured", nil))
	response := handler.dispatch(registered, request)
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "configured" {
		t.Fatalf("控制器或动作后缀未生效: status=%d body=%q", response.GetStatus(), string(response.GetBody()))
	}
}
