package route

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

type nilHTTPRouteHandler struct{}

func (*nilHTTPRouteHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// TestRouterPublicConfigurationAndMethodHelpers 验证 ThinkPHP 风格的路由配置会参与真实匹配，
// 同时覆盖 Router 公开的常用 HTTP 方法快捷入口。
func TestRouterPublicConfigurationAndMethodHelpers(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("关闭自动路由失败: %v", err)
	}
	if err := router.SetDefaultExtension(""); err != nil {
		t.Fatalf("清空默认后缀失败: %v", err)
	}
	if err := router.SetDefaultPattern(`[0-9]+`); err != nil {
		t.Fatalf("设置默认变量约束失败: %v", err)
	}
	if err := router.SetControllerLayer("admin.controller"); err != nil {
		t.Fatalf("设置控制器层失败: %v", err)
	}
	if err := router.SetDefaultPattern(""); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("空默认约束应被拒绝，实际为 %v", err)
	}
	if err := router.SetDefaultPattern("["); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("非法默认约束应被拒绝，实际为 %v", err)
	}
	if err := router.SetDefaultPattern(`(?P<id>[0-9]+)`); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("命名捕获约束应被拒绝，实际为 %v", err)
	}
	if err := router.SetControllerLayer("bad-layer"); !errors.Is(err, ErrInvalidRouteHandler) {
		t.Fatalf("非法控制器层应被拒绝，实际为 %v", err)
	}

	registrations := []struct {
		method   string
		register func() (*Route, error)
	}{
		{method: http.MethodPut, register: func() (*Route, error) { return router.Put("/items/:id", "Item/update") }},
		{method: http.MethodDelete, register: func() (*Route, error) { return router.Delete("/items/:id", "Item/delete") }},
		{method: http.MethodPatch, register: func() (*Route, error) { return router.Patch("/items/:id", "Item/patch") }},
	}
	registeredRoutes := make(map[string]*Route, len(registrations))
	for _, registration := range registrations {
		registered, err := registration.register()
		if err != nil {
			t.Fatalf("注册 %s 路由失败: %v", registration.method, err)
		}
		registeredRoutes[registration.method] = registered
	}
	for _, registration := range registrations {
		request := fwcontext.MustNewRequest(httptest.NewRequest(registration.method, "http://example.com/items/42", nil))
		matched, params, matchErr := router.Match(request)
		if matchErr != nil || matched != registeredRoutes[registration.method] || params["id"] != "42" {
			t.Fatalf("%s 路由匹配错误，路由=%#v 参数=%#v 错误=%v", registration.method, matched, params, matchErr)
		}
	}

	notMatched, _, err := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodPut, "http://example.com/items/text", nil)))
	if err != nil || notMatched != nil {
		t.Fatalf("默认数字约束不应接受文本参数，路由=%#v 错误=%v", notMatched, err)
	}
	if err = router.SetDefaultPattern(`[a-z]+`); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("冻结后修改默认约束应失败，实际为 %v", err)
	}
	if err = router.SetControllerLayer("controller"); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("冻结后修改控制器层应失败，实际为 %v", err)
	}
}

// TestRouterAutoRouteControllerLayerAccessors 验证自动路由会保留控制器层元数据，
// 且 Route 的零值读取 API 不会触发 panic。
func TestRouterAutoRouteControllerLayerAccessors(t *testing.T) {
	router := NewRouter()
	if err := router.SetControllerLayer("backend.controller"); err != nil {
		t.Fatalf("设置自动路由控制器层失败: %v", err)
	}
	matched, _, err := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/user/save", nil)))
	if err != nil || matched == nil {
		t.Fatalf("自动路由匹配失败，路由=%#v 错误=%v", matched, err)
	}
	if !matched.IsAuto() || matched.Method() != anyMethod || matched.ControllerLayer() != "backend.controller" {
		t.Fatalf("自动路由元数据错误: auto=%v method=%q layer=%q", matched.IsAuto(), matched.Method(), matched.ControllerLayer())
	}

	var nilRoute *Route
	if nilRoute.Method() != "" || nilRoute.IsAuto() || nilRoute.ControllerLayer() != "" {
		t.Fatal("空路由只读访问器必须返回零值")
	}
	standalone := &Route{method: http.MethodGet, auto: true, controllerLayer: "controller"}
	if standalone.Method() != http.MethodGet || !standalone.IsAuto() || standalone.ControllerLayer() != "controller" {
		t.Fatalf("独立路由只读访问器错误: %#v", standalone)
	}
}

// TestDynamicRouteIndexPreservesSpecificityOrder 验证关闭默认后缀后，动态前缀树会同时合并
// 多条字面量与变量候选，并继续遵循 ThinkPHP 的具体路由优先规则。
func TestDynamicRouteIndexPreservesSpecificityOrder(t *testing.T) {
	router := NewRouter()
	if err := router.SetDefaultExtension(""); err != nil {
		t.Fatalf("清空默认后缀失败: %v", err)
	}
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("关闭自动路由失败: %v", err)
	}
	paths := []string{
		"/matrix/fixed/fixed/:third",
		"/matrix/fixed/:second/fixed",
		"/matrix/:first/fixed/fixed",
		"/matrix/fixed/:second/:third",
		"/matrix/:first/fixed/:third",
		"/matrix/:first/:second/fixed",
		"/matrix/:first/:second/:third",
	}
	var expected *Route
	for index, path := range paths {
		registered, err := router.Get(path, "Matrix/show")
		if err != nil {
			t.Fatalf("注册第 %d 条动态路由失败: %v", index+1, err)
		}
		if index == 0 {
			expected = registered
		}
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结动态路由失败: %v", err)
	}
	matched, params, err := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/matrix/fixed/fixed/fixed", nil)))
	if err != nil || matched != expected || params["third"] != "fixed" {
		t.Fatalf("动态路由优先级错误，路由=%#v 参数=%#v 错误=%v", matched, params, err)
	}

	node := newRouteTrieNode()
	node.addRoute(expected)
	node.addRoute(expected)
	if len(node.routes) != 1 {
		t.Fatalf("同一路由在单个索引节点中只能保留一次，实际为 %d", len(node.routes))
	}
}

// TestMethodMissAndMiddlewareAPIs 验证按方法 MISS 的覆盖顺序，以及运行期追加路由中间件的执行语义。
func TestMethodMissAndMiddlewareAPIs(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("关闭自动路由失败: %v", err)
	}
	common, err := router.SetMiss(anyMethod, "Error/fallback")
	if err != nil {
		t.Fatalf("注册通用 MISS 失败: %v", err)
	}
	methodMiss, err := router.SetMiss(http.MethodGet, "Error/read")
	if err != nil {
		t.Fatalf("注册 GET MISS 失败: %v", err)
	}
	for _, testCase := range []struct {
		method string
		want   *Route
	}{
		{method: http.MethodGet, want: methodMiss},
		{method: http.MethodPost, want: common},
	} {
		matched, _, matchErr := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(testCase.method, "http://example.com/missing", nil)))
		if matchErr != nil || matched != testCase.want {
			t.Fatalf("%s MISS 匹配错误，路由=%#v 错误=%v", testCase.method, matched, matchErr)
		}
	}
	if _, err = router.SetMiss(http.MethodPost, "Error/write"); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("冻结后注册 MISS 应失败，实际为 %v", err)
	}

	middlewareRouter := NewRouter()
	registered, err := middlewareRouter.Get("/pipeline", "Pipeline/show")
	if err != nil {
		t.Fatalf("注册中间件测试路由失败: %v", err)
	}
	order := make([]string, 0, 2)
	appendMarker := middleware.Handler(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		order = append(order, "middleware")
		return next(request)
	})
	if err = registered.WithMiddleware(appendMarker); err != nil {
		t.Fatalf("追加路由中间件失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/pipeline", nil))
	response := registered.ExecuteMiddleware(request, func(*fwcontext.Request) *fwcontext.Response {
		order = append(order, "destination")
		return fwcontext.NewResponse().Content("ok")
	})
	if response == nil || string(response.GetBody()) != "ok" || len(order) != 2 || order[0] != "middleware" || order[1] != "destination" {
		t.Fatalf("路由中间件执行错误，顺序=%#v 响应=%#v", order, response)
	}
	if err = registered.WithMiddleware(nil); !errors.Is(err, ErrInvalidRouteMiddleware) {
		t.Fatalf("空中间件应被拒绝，实际为 %v", err)
	}
	if err = middlewareRouter.Freeze(); err != nil {
		t.Fatalf("冻结中间件路由失败: %v", err)
	}
	if err = registered.WithMiddleware(appendMarker); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("冻结后追加中间件应失败，实际为 %v", err)
	}

	var nilRoute *Route
	nilResponse := nilRoute.ExecuteMiddleware(request, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("nil route")
	})
	if string(nilResponse.GetBody()) != "nil route" {
		t.Fatalf("空路由应直接执行目标处理器，实际为 %q", nilResponse.GetBody())
	}
}

// TestRouteHandlerValidationSupportsDocumentedForms 验证控制器、ThinkPHP 风格
// 回调、框架函数和 net/http 处理器，并拒绝带类型的 nil 或非法返回签名。
func TestRouteHandlerValidationSupportsDocumentedForms(t *testing.T) {
	router := NewRouter()
	validHandlers := []HandlerFunc{
		"User/show",
		func() string { return "ThinkPHP" },
		func(name string) string { return name },
		func() (map[string]interface{}, error) { return map[string]interface{}{"ok": true}, nil },
		func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("framework") },
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		func(http.ResponseWriter, *http.Request) {},
	}
	for index, handler := range validHandlers {
		if _, err := router.Get("/valid/"+string(rune('a'+index)), handler); err != nil {
			t.Fatalf("第 %d 种公开处理器应可注册: %v", index+1, err)
		}
	}

	var frameworkNil func(*fwcontext.Request) *fwcontext.Response
	var standardNil func(http.ResponseWriter, *http.Request)
	var callbackNil func() string
	var typedNil *nilHTTPRouteHandler
	invalidHandlers := []HandlerFunc{
		nil,
		frameworkNil,
		standardNil,
		callbackNil,
		typedNil,
		"invalid",
		func() (string, int) { return "", 0 },
	}
	for index, handler := range invalidHandlers {
		if _, err := router.Get("/invalid/"+string(rune('a'+index)), handler); !errors.Is(err, ErrInvalidRouteHandler) {
			t.Fatalf("第 %d 个非法处理器应返回 ErrInvalidRouteHandler，实际为 %v", index+1, err)
		}
	}
}

// TestNamedRouteURLScalarParameterContract 验证命名路由只接受可稳定序列化的标量，
// 避免调用任意对象方法或生成 NaN、Inf 等无效 URL。
func TestNamedRouteURLScalarParameterContract(t *testing.T) {
	router := NewRouter()
	if err := router.SetDefaultExtension(""); err != nil {
		t.Fatalf("清空默认后缀失败: %v", err)
	}
	if err := router.SetDefaultPattern(`[^/]+`); err != nil {
		t.Fatalf("设置标量路由变量约束失败: %v", err)
	}
	registered, err := router.Get("/values/:value", "Value/show")
	if err != nil {
		t.Fatalf("注册命名路由失败: %v", err)
	}
	if err = registered.WithName("values.show"); err != nil {
		t.Fatalf("设置路由名称失败: %v", err)
	}
	valid := []struct {
		value interface{}
		want  string
	}{
		{value: "text", want: "/values/text"},
		{value: true, want: "/values/true"},
		{value: int(1), want: "/values/1"},
		{value: int8(-2), want: "/values/-2"},
		{value: int16(3), want: "/values/3"},
		{value: int32(-4), want: "/values/-4"},
		{value: int64(5), want: "/values/5"},
		{value: uint(6), want: "/values/6"},
		{value: uint8(7), want: "/values/7"},
		{value: uint16(8), want: "/values/8"},
		{value: uint32(9), want: "/values/9"},
		{value: uint64(10), want: "/values/10"},
		{value: float32(1.25), want: "/values/1.25"},
		{value: float64(-2.5), want: "/values/-2.5"},
		{value: json.Number("12.75"), want: "/values/12.75"},
	}
	for _, testCase := range valid {
		generated, generateErr := router.URL("values.show", map[string]interface{}{"value": testCase.value})
		if generateErr != nil || generated != testCase.want {
			t.Fatalf("标量 %#v 生成 URL 错误，实际=%q 期望=%q 错误=%v", testCase.value, generated, testCase.want, generateErr)
		}
	}
	invalid := []interface{}{
		math.NaN(),
		math.Inf(1),
		float32(math.Inf(-1)),
		json.Number(""),
		json.Number("true"),
		json.Number("01"),
		map[string]string{"value": "object"},
	}
	for _, value := range invalid {
		if _, generateErr := router.URL("values.show", map[string]interface{}{"value": value}); !errors.Is(generateErr, ErrInvalidRouteParameter) {
			t.Fatalf("非法标量 %#v 应返回 ErrInvalidRouteParameter，实际为 %v", value, generateErr)
		}
	}
}
