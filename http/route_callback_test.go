package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

type thinkPHPRouteCallbackService struct {
	Prefix string
}

// TestRouteCallbackMatchesThinkPHPReturnAndBindingContract 验证闭包可以直接返回
// 字符串或业务数据，并按路由变量声明顺序绑定标量参数。
func TestRouteCallbackMatchesThinkPHPReturnAndBindingContract(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()

	textRoute, err := router.Get("/think", func() string { return "hello,ThinkPHP8!" })
	if err != nil {
		t.Fatalf("注册无参 ThinkPHP 闭包路由失败: %v", err)
	}
	textResponse := handler.dispatch(textRoute, fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/think", nil)))
	if textResponse.GetStatus() != http.StatusOK || string(textResponse.GetBody()) != "hello,ThinkPHP8!" {
		t.Fatalf("无参闭包响应错误: status=%d body=%q", textResponse.GetStatus(), string(textResponse.GetBody()))
	}

	dataRoute, err := router.Get("/item/:id/:enabled", func(id int, enabled bool) map[string]interface{} {
		return map[string]interface{}{"id": id, "enabled": enabled}
	})
	if err != nil {
		t.Fatalf("注册参数闭包路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/item/8/true", nil))
	request.SetRoute("id", "8")
	request.SetRoute("enabled", "true")
	dataResponse := handler.dispatch(dataRoute, request)
	var payload map[string]interface{}
	if err := json.Unmarshal(dataResponse.GetBody(), &payload); err != nil {
		t.Fatalf("解析闭包 JSON 响应失败: %v", err)
	}
	if dataResponse.GetStatus() != http.StatusOK || payload["id"] != float64(8) || payload["enabled"] != true {
		t.Fatalf("闭包参数绑定错误: status=%d payload=%#v", dataResponse.GetStatus(), payload)
	}
}

// TestRouteCallbackInjectsFrameworkAndContainerDependencies 验证 ThinkPHP
// 容器调用语义：App、Request 与业务服务由类型注入，路由标量仍按声明顺序绑定。
func TestRouteCallbackInjectsFrameworkAndContainerDependencies(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	application.BindFactory("thinkPHPRouteCallbackService", func() interface{} {
		return &thinkPHPRouteCallbackService{Prefix: "order-"}
	})
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	registered, err := router.Get("/order/:id", func(
		current *framework.App,
		request *fwcontext.Request,
		service *thinkPHPRouteCallbackService,
		id int,
	) (string, error) {
		if current != application || request == nil || service == nil {
			return "", errors.New("依赖注入错误")
		}
		return service.Prefix + request.Route("id") + "-" + strconv.Itoa(id), nil
	})
	if err != nil {
		t.Fatalf("注册依赖注入闭包路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/order/8", nil))
	request.SetRoute("id", "8")
	response := handler.dispatch(registered, request)
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "order-8-8" {
		t.Fatalf("闭包依赖注入错误: status=%d body=%q", response.GetStatus(), string(response.GetBody()))
	}
}

// TestRouteCallbackSupportsVariadicDefaultsAndErrors 验证可变参数承担 Go 中的
// ThinkPHP 默认参数语义，回调错误继续进入统一异常边界。
func TestRouteCallbackSupportsVariadicDefaultsAndErrors(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	optional, err := router.Get("/hello/:name?", func(names ...string) string {
		if len(names) == 0 {
			return "ThinkPHP8"
		}
		return names[0]
	})
	if err != nil {
		t.Fatalf("注册可变参数闭包失败: %v", err)
	}
	withoutName := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/hello", nil))
	if response := handler.dispatch(optional, withoutName); string(response.GetBody()) != "ThinkPHP8" {
		t.Fatalf("闭包默认参数错误: %q", string(response.GetBody()))
	}
	withName := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/hello/ThinkGo", nil))
	withName.SetRoute("name", "ThinkGo")
	if response := handler.dispatch(optional, withName); string(response.GetBody()) != "ThinkGo" {
		t.Fatalf("闭包可变参数错误: %q", string(response.GetBody()))
	}

	failed, err := router.Get("/failed", func() error { return errors.New("private callback failure") })
	if err != nil {
		t.Fatalf("注册错误闭包失败: %v", err)
	}
	failedResponse := handler.dispatch(failed, fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/failed", nil)))
	if failedResponse.GetStatus() != http.StatusInternalServerError || string(failedResponse.GetBody()) != http.StatusText(http.StatusInternalServerError) {
		t.Fatalf("闭包错误边界错误: status=%d body=%q", failedResponse.GetStatus(), string(failedResponse.GetBody()))
	}
}
