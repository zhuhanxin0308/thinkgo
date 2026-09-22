package framework

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	frameworkRoute "github.com/zhuhanxin0308/thinkgo/v3/route"
)

// TestRouteFacadeCoversThinkPHPDefinitionAPI 验证 Route 门面的全部 HTTP
// 方法、分组、域名、重定向、资源路由与链式规则配置可以直接由业务使用。
func TestRouteFacadeCoversThinkPHPDefinitionAPI(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	routes := app.Route()
	handler := func(*Request) *Response { return NewResponse().Content("ok") }
	guard := middleware.Handler(func(request *Request, next func(*Request) *Response) *Response {
		return next(request)
	})

	if item := routes.Add(http.MethodGet, "added", handler); item.Path() != "/added" || item.Method() != http.MethodGet {
		t.Fatalf("Route.Add 结果错误: path=%q method=%q", item.Path(), item.Method())
	}
	if item := routes.Rule("all-methods", handler); item.Method() != "*" {
		t.Fatalf("无 method 的 Rule 必须匹配任意方法: %q", item.Method())
	}
	if item := routes.Rule("two-methods", handler, "GET|POST"); item.Method() != "GET|POST" {
		t.Fatalf("多方法 Rule 结果错误: %q", item.Method())
	}
	routes.Any("any", handler)
	routes.Get("get", handler)
	routes.Post("post", handler)
	routes.Put("put", handler)
	routes.Delete("delete", handler)
	routes.Patch("patch", handler)
	routes.Head("head", handler)
	routes.Options("options", handler)
	routes.View("view-only", "missing-template", map[string]interface{}{"title": "ThinkPHP"})
	routes.Redirect("legacy", "/current")
	routes.Miss(handler, http.MethodTrace)

	named := routes.Get("articles/:id", handler).
		Name("article.read").
		Pattern(map[string]string{"id": `\d+`}).
		Domain("api.example.com").
		Ext("json").
		Middleware(guard).
		WithoutMiddleware(guard)
	if named.Path() != "/articles/:id" || named.Method() != http.MethodGet {
		t.Fatalf("链式规则结果错误: path=%q method=%q", named.Path(), named.Method())
	}
	routes.Get("named/:id", handler).Name("named.read")

	routes.Resource("photos", "photo").Only("index", "read")
	routes.Resource("comments", "comment").Except("create", "edit")
	routes.Group("api", func(group *RuleGroup) {
		group.Get("get", handler)
		group.Post("post", handler)
		group.Put("put", handler)
		group.Delete("delete", handler)
		group.Patch("patch", handler)
		group.Head("head", handler)
		group.Options("options", handler)
		group.Any("any", handler)
		group.Rule("rule", handler, "GET|POST")
		group.Redirect("legacy", "/api/current", http.StatusTemporaryRedirect)
		group.Resource("users", "user").Only("index", "save")
		group.Group("v1", func(nested *RuleGroup) {
			nested.Get("status", handler)
		})
		group.Group("v2", func() {
			routes.Get("status", handler)
		})
		group.Domain("group.example.com", func(domain *RuleGroup) {
			domain.Get("domain", handler)
		})
	})
	routes.Domain("admin.example.com", func(group *RuleGroup) {
		group.Get("dashboard", handler)
	})
	routes.DomainGroup("tenant.example.com", "tenant", func(group *RuleGroup) {
		group.Get("profile", handler)
	})
	// ThinkPHP 原生调用方式不接收分组参数，闭包内继续通过 Route 门面注册。
	routes.Group("thinkphp", func() {
		routes.Get("status", func() string { return "ok" })
		routes.Group("v1", func() {
			routes.Get("version", func() string { return "v1" })
		})
	})
	routes.Domain("api.thinkphp.example.com", func() {
		routes.Get("scope", func() string { return "domain" })
	})
	routes.DomainGroup("tenant.thinkphp.example.com", "native", func() {
		routes.Get("profile", func() string { return "tenant" })
	})

	registered, err := routes.Routes()
	if err != nil || len(registered) < 36 {
		t.Fatalf("Route.Routes 未返回完整路由快照: count=%d err=%v", len(registered), err)
	}
	assertRouteMetadata := func(path, domain string) {
		t.Helper()
		for _, registeredRoute := range registered {
			if registeredRoute.Path == path && registeredRoute.Domain == domain {
				return
			}
		}
		t.Fatalf("未找到隐式分组路由: path=%q domain=%q", path, domain)
	}
	assertRouteMetadata("/thinkphp/status", "")
	assertRouteMetadata("/thinkphp/v1/version", "")
	assertRouteMetadata("/api/v2/status", "")
	assertRouteMetadata("/scope", "api.thinkphp.example.com")
	assertRouteMetadata("/native/profile", "tenant.thinkphp.example.com")
	generated, err := routes.URL("named.read", map[string]interface{}{"id": 7, "tab": "summary"})
	if err != nil || generated != "/named/7.html?tab=summary" {
		t.Fatalf("Route.URL 结果错误: url=%q err=%v", generated, err)
	}
	request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "https://example.com/base", nil))
	requestURL, err := routes.URLForRequest(request, "named.read", map[string]interface{}{"id": 8})
	if err != nil || requestURL != "/named/8.html" {
		t.Fatalf("Route.URLForRequest 结果错误: url=%q err=%v", requestURL, err)
	}
	matched, parameters, err := routes.Match(frameworkContext.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "https://example.com/named/9", nil),
	))
	if err != nil || matched == nil || parameters["id"] != "9" {
		t.Fatalf("Route.Match 结果错误: route=%#v params=%#v err=%v", matched, parameters, err)
	}
}

// TestRouteFacadeRejectsInvalidThinkPHPDefinitions 验证门面把业务路由定义
// 错误稳定地暴露为启动期 panic，并让只读 API 返回显式 error。
func TestRouteFacadeRejectsInvalidThinkPHPDefinitions(t *testing.T) {
	handler := func(*Request) *Response { return NewResponse() }
	assertPanic := func(name string, call func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("非法路由定义必须触发 panic")
				}
			}()
			call()
		})
	}

	assertPanic("nil Route", func() { (*Route)(nil).Get("path", handler) })
	assertPanic("Rule 多 method 参数", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().Rule("path", handler, "GET", "POST")
	})
	assertPanic("View template 类型", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().View("path", 7)
	})
	assertPanic("View vars 类型", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().View("path", "template", "vars")
	})
	assertPanic("View 参数过多", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().View("path", "template", nil, nil)
	})
	assertPanic("Group nil callback", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().Group("api", nil)
	})
	assertPanic("Domain nil callback", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().Domain("example.com", nil)
	})
	assertPanic("DomainGroup nil callback", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().DomainGroup("example.com", "api", nil)
	})
	assertPanic("Group 非函数 callback", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().Group("api", "invalid")
	})
	assertPanic("nil RuleItem", func() { (*RuleItem)(nil).Name("missing") })
	assertPanic("nil RuleGroup", func() { (*RuleGroup)(nil).Get("path", handler) })
	assertPanic("nil Resource", func() { (*Resource)(nil).Only("index") })
	assertPanic("资源动作缺失", func() {
		app := NewAppUninitialized(t.TempDir())
		defer app.Close()
		app.Route().Resource("users", "user").Only()
	})

	var nilRoute *Route
	if _, err := nilRoute.Routes(); !errors.Is(err, frameworkRoute.ErrInvalidRoute) {
		t.Fatalf("nil Route.Routes 必须返回 ErrInvalidRoute: %v", err)
	}
	request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	if _, _, err := nilRoute.Match(request); !errors.Is(err, frameworkRoute.ErrInvalidRoute) {
		t.Fatalf("nil Route.Match 必须返回 ErrInvalidRoute: %v", err)
	}
	if _, err := nilRoute.URL("missing", nil); !errors.Is(err, frameworkRoute.ErrInvalidRoute) {
		t.Fatalf("nil Route.URL 必须返回 ErrInvalidRoute: %v", err)
	}
	if _, err := nilRoute.URLForRequest(request, "missing", nil); !errors.Is(err, frameworkRoute.ErrInvalidRoute) {
		t.Fatalf("nil Route.URLForRequest 必须返回 ErrInvalidRoute: %v", err)
	}
	if item := (*RuleItem)(nil); item.Path() != "" || item.Method() != "" {
		t.Fatalf("nil RuleItem 查询必须返回空值: path=%q method=%q", item.Path(), item.Method())
	}
	if facade := newRouteFacade(nil, nil); facade != nil {
		t.Fatalf("nil Router 不应创建 Route 门面: %#v", facade)
	}
	if !strings.Contains(frameworkRoute.ErrInvalidRoute.Error(), "路由") {
		t.Fatal("路由定义错误必须保持可诊断文本")
	}
}

// TestRouteFacadeRestoresImplicitScopeAfterPanic 验证业务分组加载失败后仍会
// 恢复父作用域，避免后续路由被错误挂到已经退出的分组下。
func TestRouteFacadeRestoresImplicitScopeAfterPanic(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	routes := app.Route()
	func() {
		defer func() { _ = recover() }()
		routes.Group("failed", func() {
			panic("route load failed")
		})
	}()
	routes.Get("outside", func() string { return "ok" })
	registered, err := routes.Routes()
	if err != nil {
		t.Fatalf("读取作用域恢复后的路由失败: %v", err)
	}
	for _, registeredRoute := range registered {
		if registeredRoute.Path == "/outside" {
			return
		}
	}
	t.Fatalf("分组 panic 后未恢复根作用域: %#v", registered)
}
