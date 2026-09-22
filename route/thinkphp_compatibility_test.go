package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestRouterDefaultsMatchThinkPHP8 验证无项目配置时的路由默认值与
// ThinkPHP 8.1.4 config/route.php 保持一致。
func TestRouterDefaultsMatchThinkPHP8(t *testing.T) {
	router := NewRouter()

	if !router.autoRoute {
		t.Fatal("ThinkPHP 默认 url_route_must=false，应启用自动路由")
	}
	if router.caseSensitive {
		t.Fatal("ThinkPHP 默认 URL 不区分大小写")
	}
	if router.completeMatch {
		t.Fatal("ThinkPHP 默认不启用 route_complete_match")
	}
	if router.removeSlash {
		t.Fatal("ThinkPHP 默认不移除末尾斜杠")
	}
	if router.defaultController != "Index" || router.defaultAction != "index" {
		t.Fatalf("默认控制器动作错误: %s/%s", router.defaultController, router.defaultAction)
	}
	if router.controllerLayer != "controller" {
		t.Fatalf("默认控制器层错误: %q", router.controllerLayer)
	}
	if router.defaultExtension != "html" {
		t.Fatalf("默认 URL 后缀错误: %q", router.defaultExtension)
	}
	if router.defaultPattern != `[\w\.]+` {
		t.Fatalf("默认路由变量规则错误: %q", router.defaultPattern)
	}
}

// TestRouterDefaultPatternMatchesThinkPHP8 验证没有显式 Pattern 的路由变量
// 使用 ThinkPHP default_route_pattern，而不是接受任意非空路径段。
func TestRouterDefaultPatternMatchesThinkPHP8(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/files/:name", "file/read"); err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	for _, testCase := range []struct {
		path string
		want bool
	}{
		{path: "/files/report_1.txt", want: true},
		{path: "/files/report-1.txt", want: false},
	} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+testCase.path, nil))
		matched, _, err := router.Match(request)
		if err != nil {
			t.Fatalf("匹配 %q 失败: %v", testCase.path, err)
		}
		if (matched != nil) != testCase.want {
			t.Fatalf("路径 %q 匹配状态错误: route=%#v", testCase.path, matched)
		}
	}
}

// TestRouterMethodMissFallsBackToThinkPHPURLDispatch 验证显式路由的方法不匹配时，
// 默认继续 URL 调度；HEAD 不隐式复用 GET，OPTIONS 使用 ThinkPHP 固定自动响应。
func TestRouterMethodMissFallsBackToThinkPHPURLDispatch(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/items", "item/show"); err != nil {
		t.Fatalf("注册 GET 路由失败: %v", err)
	}

	for _, method := range []string{http.MethodPost, http.MethodHead} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(method, "http://example.com/items", nil))
		matched, _, err := router.Match(request)
		if err != nil || matched == nil || !matched.IsAuto() || matched.Handler() != "items/index" {
			t.Fatalf("%s 方法不匹配后应继续 URL 调度: route=%#v err=%v", method, matched, err)
		}
	}

	optionsRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodOptions, "http://example.com/items", nil))
	optionsRoute, _, err := router.Match(optionsRequest)
	if err != nil || optionsRoute == nil {
		t.Fatalf("自动 OPTIONS 路由错误: route=%#v err=%v", optionsRoute, err)
	}
	handler, ok := optionsRoute.Handler().(func(*fwcontext.Request) *fwcontext.Response)
	if !ok {
		t.Fatalf("自动 OPTIONS 处理器类型错误: %T", optionsRoute.Handler())
	}
	response := handler(optionsRequest)
	if response.GetStatus() != http.StatusNoContent || response.GetHeader("Allow") != "GET, POST, PUT, DELETE" {
		t.Fatalf("ThinkPHP 自动 OPTIONS 默认值错误: status=%d allow=%q", response.GetStatus(), response.GetHeader("Allow"))
	}
}

// TestRouterMustModeDoesNotCreateMethodNotAllowed 验证 url_route_must=true 时，
// 方法不匹配与普通未匹配一样交给上层生成 404，而不是自定义 405。
func TestRouterMustModeDoesNotCreateMethodNotAllowed(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("开启强制路由失败: %v", err)
	}
	if _, err := router.Get("/items", "item/show"); err != nil {
		t.Fatalf("注册 GET 路由失败: %v", err)
	}
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodOptions} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(method, "http://example.com/items", nil))
		matched, _, err := router.Match(request)
		if err != nil || matched != nil {
			t.Fatalf("强制路由下 %s 方法不匹配应保持未命中: route=%#v err=%v", method, matched, err)
		}
	}
}

// TestResourceHandlersUseThinkPHPSlashNotation 验证资源路由生成的处理器
// 与普通业务路由一样使用 controller/action，而不是暴露旧分隔符。
func TestResourceHandlersUseThinkPHPSlashNotation(t *testing.T) {
	router := NewRouter()
	resource, err := router.Resource("users", "user")
	if err != nil {
		t.Fatalf("注册资源路由失败: %v", err)
	}
	want := map[string]string{
		"index":  "user/index",
		"create": "user/create",
		"save":   "user/save",
		"read":   "user/read",
		"edit":   "user/edit",
		"update": "user/update",
		"delete": "user/delete",
	}
	for action, handler := range want {
		registered := resource.routes[action]
		if registered == nil || registered.Handler() != handler {
			t.Fatalf("资源动作 %s 的处理器错误: route=%#v want=%q", action, registered, handler)
		}
	}
}
