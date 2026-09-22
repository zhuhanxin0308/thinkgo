package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// mwTestController 用于验证控制器级中间件声明会被分发器实际应用。
type mwTestController struct {
	framework.Controller
}

// Initialize 对应 ThinkPHP BaseController::initialize，且仅对 Guarded 动作声明中间件。
func (c *mwTestController) Initialize() {
	if c.App == nil || c.Request == nil {
		panic("Initialize 调用前必须完成 App 和 Request 注入")
	}
	c.SetMiddleware(framework.ControllerMiddleware{Name: "tap", Only: []string{"Guarded"}})
}

// Guarded 受控制器中间件保护的动作。
func (c *mwTestController) Guarded(req *fwcontext.Request) *fwcontext.Response {
	return fwcontext.NewResponse().Content("guarded-action")
}

// Open 未被 Only 命中的动作，不应触发控制器中间件。
func (c *mwTestController) Open(req *fwcontext.Request) *fwcontext.Response {
	return fwcontext.NewResponse().Content("open-action")
}

// newControllerTestApp 构建带容器的最小应用，供控制器分发相关测试使用。
func newControllerTestApp(t *testing.T) *framework.App {
	t.Helper()
	return newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
}

// TestControllerMiddlewareApplied 验证 Only 命中的动作会执行控制器声明的中间件。
func TestControllerMiddlewareApplied(t *testing.T) {
	app := newControllerTestApp(t)
	app.BindFactory("MwCtrl", reflect.TypeOf(mwTestController{}))

	// 注册中间件别名：命中时为响应打上标记头。
	mustHTTPMiddleware(t, app).Alias("tap", func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		resp := next(req)
		if resp != nil {
			resp.Header("X-Controller-Mw", "1")
		}
		return resp
	})

	mustHTTPRoute(t, app).Get("/guarded", "MwCtrl@Guarded")
	mustHTTPRoute(t, app).Get("/open", "MwCtrl@Open")

	handler := newTestHTTPHandler(t, app)

	// Only 命中：中间件应执行。
	guardedRec := httptest.NewRecorder()
	handler.ServeHTTP(guardedRec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/guarded", nil))
	if guardedRec.Body.String() != "guarded-action" {
		t.Fatalf("受控动作响应内容不正确: %q", guardedRec.Body.String())
	}
	if guardedRec.Header().Get("X-Controller-Mw") != "1" {
		t.Fatal("控制器级中间件未对 Only 命中的动作生效")
	}

	// 未命中 Only：中间件不应执行。
	openRec := httptest.NewRecorder()
	handler.ServeHTTP(openRec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/open", nil))
	if openRec.Body.String() != "open-action" {
		t.Fatalf("普通动作响应内容不正确: %q", openRec.Body.String())
	}
	if openRec.Header().Get("X-Controller-Mw") != "" {
		t.Fatal("控制器级中间件不应对未命中 Only 的动作生效")
	}
}

// TestControllerMiddlewareMissingAliasFailsClosed 验证控制器声明的保护中间件缺失时返回 500，而不是绕过保护继续执行动作。
func TestControllerMiddlewareMissingAliasFailsClosed(t *testing.T) {
	app := newControllerTestApp(t)
	app.BindFactory("MwCtrl", reflect.TypeOf(mwTestController{}))
	if _, err := mustHTTPRoute(t, app).Get("/guarded", "MwCtrl@Guarded"); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(
		recorder,
		httptest.NewRequest(stdhttp.MethodGet, "http://example.com/guarded", nil),
	)
	if recorder.Code != stdhttp.StatusInternalServerError {
		t.Fatalf("缺失控制器中间件别名必须失败关闭，实际状态码为 %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "guarded-action") {
		t.Fatalf("缺失保护中间件时不得执行控制器动作，响应为 %q", recorder.Body.String())
	}
}

// TestControllerMiddlewareActionFilters 验证 Only、Except 和默认全量三种过滤语义。
func TestControllerMiddlewareActionFilters(t *testing.T) {
	tests := []struct {
		name        string
		declaration framework.ControllerMiddleware
		action      string
		expected    bool
	}{
		{name: "only matches", declaration: framework.ControllerMiddleware{Only: []string{"Show"}}, action: "show", expected: true},
		{name: "only misses", declaration: framework.ControllerMiddleware{Only: []string{"Show"}}, action: "Edit", expected: false},
		{name: "except blocks", declaration: framework.ControllerMiddleware{Except: []string{"Delete"}}, action: "delete", expected: false},
		{name: "except allows", declaration: framework.ControllerMiddleware{Except: []string{"Delete"}}, action: "Show", expected: true},
		{name: "default allows", declaration: framework.ControllerMiddleware{}, action: "Show", expected: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := controllerMiddlewareApplies(test.declaration, test.action); actual != test.expected {
				t.Fatalf("过滤结果错误，期望 %v，实际 %v", test.expected, actual)
			}
		})
	}
	if err := validateControllerActionFilter([]string{"Show", "show"}); err == nil {
		t.Fatal("动作过滤列表不应接受大小写重复项")
	}
	if err := validateControllerActionFilter([]string{"bad-action"}); err == nil {
		t.Fatal("动作过滤列表不应接受非法标识符")
	}
}
