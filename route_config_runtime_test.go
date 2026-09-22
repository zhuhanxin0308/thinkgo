package framework

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestRouteConfigIsAppliedToRuntimeRouter 验证 route.json 的自动路由、默认控制器动作和控制器层配置真正参与匹配。
func TestRouteConfigIsAppliedToRuntimeRouter(t *testing.T) {
	app := &App{config: config.NewConfig(), route: route.NewRouter()}
	app.config.Set("route", map[string]interface{}{
		"url_route_must":       false,
		"url_case_sensitive":   false,
		"route_complete_match": false,
		"default_controller":   "Home",
		"default_action":       "main",
		"controller_layer":     "admin",
		"url_html_suffix":      "html",
		"remove_slash":         false,
	})

	app.applyRouteConfig()
	if startupErr := app.StartupError(); startupErr != nil {
		t.Fatalf("合法路由配置不应产生启动错误: %v", startupErr)
	}
	registered, err := app.route.Get("/Users", "Users/Index")
	if err != nil {
		t.Fatalf("注册配置路由失败: %v", err)
	}
	if err = registered.WithName("users.index"); err != nil {
		t.Fatalf("设置命名路由失败: %v", err)
	}

	matched, _, err := app.route.Match(context.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil)))
	if err != nil || matched == nil {
		t.Fatalf("自动路由配置未匹配根路径: route=%#v err=%v", matched, err)
	}
	if handler, ok := matched.Handler().(string); !ok || handler != "Home/main" {
		t.Fatalf("默认控制器和动作未生效: handler=%#v", matched.Handler())
	}
	if matched.ControllerLayer() != "admin" {
		t.Fatalf("控制器层未生效: %q", matched.ControllerLayer())
	}

	if matched, _, err = app.route.Match(context.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/extra", nil))); err != nil || matched == nil {
		t.Fatalf("大小写不敏感或前缀匹配未生效: route=%#v err=%v", matched, err)
	}
	if generated, err := app.route.URL("users.index", nil); err != nil || generated != "/Users.html" {
		t.Fatalf("默认 URL 后缀未生效: url=%q err=%v", generated, err)
	}
}
