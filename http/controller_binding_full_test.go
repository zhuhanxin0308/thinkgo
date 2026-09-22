package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

type controllerBindingDependency struct {
	value string
}

type fullControllerBindingController struct{}

func (*fullControllerBindingController) Query(first string, values []int) string {
	return fmt.Sprintf("%s:%v", first, values)
}

func (*fullControllerBindingController) Optional(name string, count *int) string {
	if count == nil {
		return name + ":nil"
	}
	return fmt.Sprintf("%s:%d", name, *count)
}

func (*fullControllerBindingController) Dependency(dependency *controllerBindingDependency) string {
	return dependency.value
}

type NestedBaseController struct {
	framework.Controller
}

type NestedControllerBindingController struct {
	*NestedBaseController
}

func (controller *NestedControllerBindingController) Index() string {
	if controller.NestedBaseController == nil || controller.App == nil || controller.Request == nil {
		return "missing context"
	}
	return "injected"
}

// TestControllerActionQuerySliceAndOptionalPointerBinding 验证 ThinkPHP 的 get
// 动作绑定会稳定排序参数名、保留同名多值，并把缺失的可选指针绑定为 nil。
func TestControllerActionQuerySliceAndOptionalPointerBinding(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := application.BindFactory("FullBinding", func() interface{} { return &fullControllerBindingController{} }); err != nil {
		t.Fatalf("绑定控制器失败: %v", err)
	}
	handler := newTestHTTPHandler(t, application)

	queryRoute := routeForDispatchTest(t, "FullBinding@Query")
	queryRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/query?values=1&values=2&first=ThinkPHP", nil))
	queryResponse := handler.dispatch(queryRoute, queryRequest)
	if queryResponse.GetStatus() != http.StatusOK || string(queryResponse.GetBody()) != "ThinkPHP:[1 2]" {
		t.Fatalf("GET 动作参数绑定错误: status=%d body=%q", queryResponse.GetStatus(), queryResponse.GetBody())
	}

	router := route.NewRouter()
	if err := router.SetCompleteMatch(true); err != nil {
		t.Fatalf("启用完整路由匹配失败: %v", err)
	}
	if err := router.SetDefaultExtension(""); err != nil {
		t.Fatalf("清空默认路由后缀失败: %v", err)
	}
	if _, err := router.Get("/optional/:name/:count?", "FullBinding@Optional"); err != nil {
		t.Fatalf("注册可选参数路由失败: %v", err)
	}
	for _, testCase := range []struct {
		path string
		want string
	}{
		{path: "/optional/ThinkPHP", want: "ThinkPHP:nil"},
		{path: "/optional/ThinkPHP/8", want: "ThinkPHP:8"},
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
			t.Fatalf("可选指针参数绑定错误: path=%s status=%d body=%q", testCase.path, response.GetStatus(), response.GetBody())
		}
	}
}

// TestControllerActionResolvesContainerDependency 验证业务动作可按类型直接声明
// 应用容器依赖，开发者无需在控制器中手工查找服务。
func TestControllerActionResolvesContainerDependency(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := application.BindFactory("FullBinding", func() interface{} { return &fullControllerBindingController{} }); err != nil {
		t.Fatalf("绑定控制器失败: %v", err)
	}
	if err := application.Instance("controllerBindingDependency", &controllerBindingDependency{value: "resolved"}); err != nil {
		t.Fatalf("绑定动作依赖失败: %v", err)
	}
	handler := newTestHTTPHandler(t, application)
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/dependency", nil))
	response := handler.dispatch(routeForDispatchTest(t, "FullBinding@Dependency"), request)
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "resolved" {
		t.Fatalf("容器动作依赖解析错误: status=%d body=%q", response.GetStatus(), response.GetBody())
	}
}

// TestNestedBaseControllerReceivesRequestContext 验证多层匿名嵌入 BaseController
// 时，框架仍会在 Initialize/动作前自动构造嵌入指针并注入 App、Request。
func TestNestedBaseControllerReceivesRequestContext(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := application.BindFactory("Nested", func() interface{} { return &NestedControllerBindingController{} }); err != nil {
		t.Fatalf("绑定嵌套控制器失败: %v", err)
	}
	handler := newTestHTTPHandler(t, application)
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/nested", nil))
	response := handler.dispatch(routeForDispatchTest(t, "Nested@Index"), request)
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "injected" {
		t.Fatalf("嵌套基础控制器注入错误: status=%d body=%q", response.GetStatus(), response.GetBody())
	}
}

// TestControllerActionScalarConversionContract 验证动作参数支持的标量、指针和切片
// 转换边界，确保溢出或复杂对象不会以零值静默进入业务方法。
func TestControllerActionScalarConversionContract(t *testing.T) {
	tests := []struct {
		name   string
		raw    interface{}
		target reflect.Type
		want   interface{}
	}{
		{name: "string", raw: int8(8), target: reflect.TypeOf(""), want: "8"},
		{name: "bool", raw: "true", target: reflect.TypeOf(false), want: true},
		{name: "int", raw: uint8(1), target: reflect.TypeOf(int(0)), want: int(1)},
		{name: "int8", raw: "2", target: reflect.TypeOf(int8(0)), want: int8(2)},
		{name: "int16", raw: int32(3), target: reflect.TypeOf(int16(0)), want: int16(3)},
		{name: "int32", raw: "4", target: reflect.TypeOf(int32(0)), want: int32(4)},
		{name: "int64", raw: float32(5), target: reflect.TypeOf(int64(0)), want: int64(5)},
		{name: "uint", raw: "6", target: reflect.TypeOf(uint(0)), want: uint(6)},
		{name: "uint8", raw: int(7), target: reflect.TypeOf(uint8(0)), want: uint8(7)},
		{name: "uint16", raw: "8", target: reflect.TypeOf(uint16(0)), want: uint16(8)},
		{name: "uint32", raw: uint64(9), target: reflect.TypeOf(uint32(0)), want: uint32(9)},
		{name: "uint64", raw: "10", target: reflect.TypeOf(uint64(0)), want: uint64(10)},
		{name: "float32", raw: "1.25", target: reflect.TypeOf(float32(0)), want: float32(1.25)},
		{name: "float64", raw: int16(2), target: reflect.TypeOf(float64(0)), want: float64(2)},
		{name: "pointer", raw: "11", target: reflect.TypeOf((*int)(nil)), want: intPointer(11)},
		{name: "slice", raw: []interface{}{"12", int8(13)}, target: reflect.TypeOf([]int{}), want: []int{12, 13}},
		{name: "single slice", raw: "14", target: reflect.TypeOf([]int{}), want: []int{14}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			converted, err := convertControllerActionValue(testCase.raw, testCase.target)
			if err != nil {
				t.Fatalf("动作参数转换失败: %v", err)
			}
			if !reflect.DeepEqual(converted.Interface(), testCase.want) {
				t.Fatalf("动作参数转换错误: want=%#v got=%#v", testCase.want, converted.Interface())
			}
		})
	}

	if converted, err := convertControllerActionValue(nil, reflect.TypeOf((*int)(nil))); err != nil || !converted.IsNil() {
		t.Fatalf("nil 指针参数转换错误: value=%#v err=%v", converted, err)
	}
	if converted, err := convertControllerActionValue(nil, reflect.TypeOf([]string{})); err != nil || !converted.IsNil() {
		t.Fatalf("nil 切片参数转换错误: value=%#v err=%v", converted, err)
	}
	invalid := []struct {
		raw    interface{}
		target reflect.Type
	}{
		{raw: nil, target: reflect.TypeOf(0)},
		{raw: "128", target: reflect.TypeOf(int8(0))},
		{raw: "-1", target: reflect.TypeOf(uint(0))},
		{raw: "text", target: reflect.TypeOf(float64(0))},
		{raw: map[string]string{"value": "object"}, target: reflect.TypeOf("")},
		{raw: "value", target: reflect.TypeOf(struct{}{})},
		{raw: "value", target: nil},
	}
	for index, testCase := range invalid {
		if _, err := convertControllerActionValue(testCase.raw, testCase.target); err == nil {
			t.Fatalf("第 %d 个非法动作参数必须返回错误", index+1)
		}
	}
}

func intPointer(value int) *int {
	return &value
}
