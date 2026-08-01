package http

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "thinkgo/framework/context"
)

// testDispatchController 用于验证控制器分发计划缓存。
type testDispatchController struct{}

// Init 模拟带初始化方法的控制器。
func (c *testDispatchController) Init(app interface{}, req *fwcontext.Request) {}

// Show 模拟带请求参数的动作方法。
func (c *testDispatchController) Show(req *fwcontext.Request) *fwcontext.Response {
	return fwcontext.NewResponse().Content("ok")
}

type errorDispatchController struct{}

func (c *errorDispatchController) Fail(req *fwcontext.Request) (*fwcontext.Response, error) {
	return nil, errors.New("private action failure")
}

type invalidSignatureController struct{}

func (c *invalidSignatureController) Bad(first, second string) string {
	return first + second
}

// TestDispatchCachesControllerPlan 验证第一次分发后会缓存控制器方法计划，后续请求复用同一计划。
func TestDispatchCachesControllerPlan(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.BindFactory("TestDispatchController", func() interface{} {
		return &testDispatchController{}
	})

	handler := newTestHTTPHandler(t, app)
	matchedRoute := routeForDispatchTest(t, "TestDispatchController@Show")
	req := fwcontext.MustNewRequest(httptest.NewRequest("GET", "http://example.com/test", nil))

	firstResp := handler.dispatch(matchedRoute, req)
	if string(firstResp.GetBody()) != "ok" {
		t.Fatalf("第一次分发返回内容不正确，实际为 %q", string(firstResp.GetBody()))
	}

	if len(handler.dispatchPlans) != 1 {
		t.Fatalf("第一次分发后应缓存 1 个控制器计划，实际为 %d", len(handler.dispatchPlans))
	}
	firstPlan := handler.dispatchPlans["TestDispatchController@Show"]
	if firstPlan == nil {
		t.Fatal("第一次分发后应缓存 TestDispatchController@Show 的控制器计划")
	}

	secondResp := handler.dispatch(matchedRoute, req)
	if string(secondResp.GetBody()) != "ok" {
		t.Fatalf("第二次分发返回内容不正确，实际为 %q", string(secondResp.GetBody()))
	}

	secondPlan := handler.dispatchPlans["TestDispatchController@Show"]
	if secondPlan == nil {
		t.Fatal("第二次分发后缓存计划不应丢失")
	}
	if firstPlan != secondPlan {
		t.Fatal("相同控制器动作的分发计划应被复用，而不是重复解析反射方法")
	}
}

// TestDispatchCachesAutoRoutePlan 验证自动路由也复用控制器反射计划，避免热路径重复解析签名。
func TestDispatchCachesAutoRoutePlan(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.BindFactory("AutoDispatch", func() interface{} {
		return &testDispatchController{}
	})
	router := mustHTTPRoute(t, app)
	if err := router.EnableAutoRoute(true); err != nil {
		t.Fatalf("启用自动路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest("GET", "http://example.com/autoDispatch/show", nil))
	matchedRoute, _, err := router.Match(req)
	if err != nil || matchedRoute == nil {
		t.Fatalf("匹配自动路由失败: route=%#v err=%v", matchedRoute, err)
	}
	if response := handler.dispatch(matchedRoute, req); string(response.GetBody()) != "ok" {
		t.Fatalf("第一次自动路由分发结果错误: %q", string(response.GetBody()))
	}
	firstPlan := handler.dispatchPlans["AutoDispatch@Show"]
	if firstPlan == nil {
		t.Fatal("第一次自动路由分发后应缓存控制器计划")
	}
	if response := handler.dispatch(matchedRoute, req); string(response.GetBody()) != "ok" {
		t.Fatalf("第二次自动路由分发结果错误: %q", string(response.GetBody()))
	}
	if secondPlan := handler.dispatchPlans["AutoDispatch@Show"]; secondPlan != firstPlan {
		t.Fatal("自动路由后续请求应复用已缓存的控制器计划")
	}
}

// TestDispatchHandlesActionErrorsAndRejectsInvalidSignatures 验证反射动作错误被处理，非法签名不会在 Call 时 panic。
func TestDispatchHandlesActionErrorsAndRejectsInvalidSignatures(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.BindFactory("ErrorController", func() interface{} { return &errorDispatchController{} })
	app.BindFactory("InvalidController", func() interface{} { return &invalidSignatureController{} })
	handler := newTestHTTPHandler(t, app)
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/test", nil))

	for name, routeHandler := range map[string]string{
		"动作返回错误": "ErrorController@Fail",
		"动作签名非法": "InvalidController@Bad",
	} {
		t.Run(name, func(t *testing.T) {
			resp := handler.dispatch(routeForDispatchTest(t, routeHandler), req)
			if resp.GetStatus() != http.StatusInternalServerError {
				t.Fatalf("分发失败应返回 500，实际为 %d", resp.GetStatus())
			}
			if string(resp.GetBody()) != http.StatusText(http.StatusInternalServerError) {
				t.Fatalf("生产响应不应泄露反射或动作错误，实际为 %q", string(resp.GetBody()))
			}
		})
	}
}
