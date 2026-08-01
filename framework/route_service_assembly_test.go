package framework

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/route"
)

// TestRouteAssemblyUsesContainerRouter 验证路由配置装配到容器中的路由器，而不是固定写入旧的公开字段。
func TestRouteAssemblyUsesContainerRouter(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })

	if err := app.config.Set("route", map[string]interface{}{
		"url_case_sensitive":   false,
		"route_complete_match": true,
		"remove_slash":         true,
		"url_route_must":       false,
		"default_controller":   "Home",
		"default_action":       "index",
	}); err != nil {
		t.Fatalf("设置测试路由配置失败: %v", err)
	}
	if err := app.config.Set("app.operational_routes_enable", true); err != nil {
		t.Fatalf("启用测试运维路由失败: %v", err)
	}
	replacement := route.NewRouter()
	app.Instance(string(ServiceRoute), replacement)

	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化测试应用失败: %v", err)
	}

	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	matched, _, err := replacement.Match(request)
	if err != nil || matched == nil {
		t.Fatalf("容器路由器未应用自动路由配置: route=%#v err=%v", matched, err)
	}
	if handler, ok := matched.Handler().(string); !ok || handler != "Home@Index" {
		t.Fatalf("容器路由器未应用默认控制器和动作: handler=%#v", matched.Handler())
	}
	if app.route != replacement {
		t.Fatal("显式替换后的路由服务应与应用内部快照保持一致")
	}

	routes, err := replacement.Routes()
	if err != nil {
		t.Fatalf("读取容器路由器快照失败: %v", err)
	}
	if !hasRoutePath(routes, OperationalLivenessPath) {
		t.Fatal("运维路由应注册到容器中的路由器")
	}
}

func hasRoutePath(routes []route.RouteInfo, target string) bool {
	for _, current := range routes {
		if current.Path == target {
			return true
		}
	}
	return false
}
