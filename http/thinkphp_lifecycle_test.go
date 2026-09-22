package http

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

type thinkPHPInitializeController struct {
	framework.Controller
	steps *[]string
}

type thinkPHPRequestMetadataController struct {
	framework.Controller
	metadata *[][]string
}

// Initialize 记录 ThinkPHP 在控制器构造阶段即可读取的调度元数据。
func (controller *thinkPHPRequestMetadataController) Initialize() {
	*controller.metadata = append(*controller.metadata, []string{
		controller.Request.Layer(),
		controller.Request.Controller(),
		controller.Request.Action(),
	})
}

// Index 记录动作阶段的调度元数据。
func (controller *thinkPHPRequestMetadataController) Index() string {
	*controller.metadata = append(*controller.metadata, []string{
		controller.Request.Layer(),
		controller.Request.Controller(),
		controller.Request.Action(),
	})
	return "metadata"
}

// Initialize 对应 ThinkPHP BaseController 构造阶段调用的 initialize 钩子。
func (controller *thinkPHPInitializeController) Initialize() {
	*controller.steps = append(*controller.steps, "initialize")
	if controller.App == nil || controller.Request == nil {
		panic("Initialize 调用前必须完成 App 和 Request 注入")
	}
}

// Index 记录动作执行顺序。
func (controller *thinkPHPInitializeController) Index() string {
	*controller.steps = append(*controller.steps, "action")
	return "initialized"
}

// TestHttpLazilyInitializesAppAndUsesThinkPHPLifecycleOrder 验证 NewHttp 不提前
// 初始化应用，并在请求中按 AppInit、HttpRun、RouteLoaded、HttpEnd 的顺序运行。
func TestHttpLazilyInitializesAppAndUsesThinkPHPLifecycleOrder(t *testing.T) {
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	app := framework.NewApp(basePath)
	t.Cleanup(func() { _ = app.Close() })

	events := make([]string, 0, 4)
	routeCalled := false
	for _, eventName := range []string{
		event.EventAppInit,
		event.EventHttpRun,
		event.EventRouteLoaded,
		event.EventHttpEnd,
	} {
		name := eventName
		if err := app.Event().Listen(name, &event.SimpleListener{Handler: func(event.Event) error {
			events = append(events, name)
			return nil
		}}); err != nil {
			t.Fatalf("监听生命周期事件 %q 失败: %v", name, err)
		}
	}
	if err := app.RegisterRouteLoader(func(application *framework.App) error {
		application.Route().Get("/", func(*fwcontext.Request) *fwcontext.Response {
			routeCalled = true
			return fwcontext.NewResponse().Content("ThinkGo")
		})
		return nil
	}); err != nil {
		t.Fatalf("注册路由加载器失败: %v", err)
	}

	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("构造 HTTP 内核失败: %v", err)
	}
	if app.Initialized() {
		t.Fatal("NewHttp 不得提前初始化 App")
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "ThinkGo" {
		routes, _ := app.Route().Routes()
		matchRequest, requestErr := fwcontext.NewRequest(httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
		matched, _, matchErr := app.Route().Match(matchRequest)
		t.Fatalf("HTTP 响应错误: status=%d body=%q initialize=%v startup=%v events=%#v route_called=%t routes=%#v request_err=%v matched=%#v match_err=%v", recorder.Code, recorder.Body.String(), handler.initializeErr, app.StartupError(), events, routeCalled, routes, requestErr, matched, matchErr)
	}
	if !app.Initialized() {
		t.Fatal("第一次 Http 请求必须初始化 App")
	}
	wantEvents := []string{
		event.EventAppInit,
		event.EventHttpRun,
		event.EventRouteLoaded,
		event.EventHttpEnd,
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("ThinkPHP 生命周期顺序错误: want=%#v got=%#v", wantEvents, events)
	}
}

// TestControllerInitializeRunsAfterInjectionAndBeforeAction 验证控制器实例与
// ThinkPHP BaseController 一样，先注入 App、Request，再执行 Initialize，最后调用动作。
func TestControllerInitializeRunsAfterInjectionAndBeforeAction(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	steps := make([]string, 0, 2)
	application.BindFactory("Initialize", func() interface{} {
		return &thinkPHPInitializeController{steps: &steps}
	})
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	registered, err := router.Get("/initialize", "initialize/index")
	if err != nil {
		t.Fatalf("注册初始化测试路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/initialize", nil))
	response := handler.dispatch(registered, request)
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "initialized" {
		t.Fatalf("控制器响应错误: status=%d body=%q", response.GetStatus(), string(response.GetBody()))
	}
	if want := []string{"initialize", "action"}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("控制器初始化顺序错误: want=%#v got=%#v", want, steps)
	}
}

// TestControllerDispatchMetadataExistsBeforeInitialize 验证 Request 的 layer、
// controller、action 在 Initialize 运行前已经按 ThinkPHP parseDispatch 写入。
func TestControllerDispatchMetadataExistsBeforeInitialize(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	metadata := make([][]string, 0, 2)
	application.BindFactory("Admin.Metadata", func() interface{} {
		return &thinkPHPRequestMetadataController{metadata: &metadata}
	})
	handler := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	registered, err := router.Get("/metadata", "admin/metadata/index")
	if err != nil {
		t.Fatalf("注册调度元数据测试路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/metadata", nil))
	response := handler.dispatch(registered, request)
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "metadata" {
		t.Fatalf("控制器响应错误: status=%d body=%q", response.GetStatus(), string(response.GetBody()))
	}
	want := [][]string{{"admin", "admin.Metadata", "index"}, {"admin", "admin.Metadata", "index"}}
	if !reflect.DeepEqual(metadata, want) {
		t.Fatalf("ThinkPHP 调度元数据错误: want=%#v got=%#v", want, metadata)
	}
}
