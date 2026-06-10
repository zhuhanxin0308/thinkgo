package http

import (
	"net/http/httptest"
	"testing"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/route"
)

// testDispatchController 用于验证控制器分发计划缓存。
type testDispatchController struct{}

// Init 模拟带初始化方法的控制器。
func (c *testDispatchController) Init(app interface{}, req *fwcontext.Request) {}

// Show 模拟带请求参数的动作方法。
func (c *testDispatchController) Show(req *fwcontext.Request) *fwcontext.Response {
	return fwcontext.NewResponse().Content("ok")
}

// TestDispatchCachesControllerPlan 验证第一次分发后会缓存控制器方法计划，后续请求复用同一计划。
func TestDispatchCachesControllerPlan(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	app.Container = framework.NewContainer()
	app.BindFactory("TestDispatchController", func() interface{} {
		return &testDispatchController{}
	})

	handler := NewHttp(app)
	matchedRoute := &route.Route{Handler: "TestDispatchController@Show"}
	req := fwcontext.NewRequest(httptest.NewRequest("GET", "http://example.com/test", nil))

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
