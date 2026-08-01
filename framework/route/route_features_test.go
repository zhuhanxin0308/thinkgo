package route

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

func TestRouterOptionalParamAndNamedURL(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users/:id/:tab?", "User@Show")
	if err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	if err = registered.WithName("user.show"); err != nil {
		t.Fatalf("设置路由名称失败: %v", err)
	}

	withoutOptional := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/18", nil))
	matched, params := matchForTest(t, router, withoutOptional)
	if matched == nil || params["id"] != "18" || params["tab"] != "" {
		t.Fatalf("可选参数省略时匹配不正确，实际路由=%#v 参数=%#v", matched, params)
	}

	withOptional := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/18/profile", nil))
	matched, params = matchForTest(t, router, withOptional)
	if matched == nil || params["tab"] != "profile" {
		t.Fatalf("可选参数存在时匹配不正确，实际参数=%#v", params)
	}

	builtURL, err := router.URL("user.show", map[string]interface{}{"id": 18, "tab": "profile", "keyword": "go"})
	if err != nil {
		t.Fatalf("命名路由 URL 生成失败: %v", err)
	}
	if builtURL != "/users/18/profile?keyword=go" {
		t.Fatalf("命名路由 URL 不正确，实际为 %q", builtURL)
	}
	builtURL, err = router.URL("user.show", map[string]interface{}{"id": 18, "tab": ""})
	if err != nil {
		t.Fatalf("省略空可选参数时生成 URL 失败: %v", err)
	}
	if builtURL != "/users/18" {
		t.Fatalf("空可选参数不应回流到查询字符串，实际为 %q", builtURL)
	}
	builtURL, err = router.URL("user.show", map[string]interface{}{"id": 18, "keyword": []string{}})
	if err != nil || builtURL != "/users/18" {
		t.Fatalf("空查询数组不应生成孤立问号，URL=%q 错误=%v", builtURL, err)
	}
}

func TestRouterGroupIsScopedAndMiddlewareRemovalFailsClosed(t *testing.T) {
	router := NewRouter()
	groupMiddleware := middleware.Handler(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return next(req)
	})
	routeMiddleware := middleware.Handler(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return next(req)
	})

	err := router.DomainGroup("api.example.com", "/api", func(group *Group) error {
		registered, registerErr := group.Get("/users", "User@Index", routeMiddleware)
		if registerErr != nil {
			return registerErr
		}
		return registered.WithoutMiddleware(groupMiddleware)
	}, groupMiddleware)
	if err != nil {
		t.Fatalf("注册域名分组失败: %v", err)
	}

	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://api.example.com/api/users", nil))
	matched, _ := matchForTest(t, router, req)
	if matched == nil {
		t.Fatal("DomainGroup 注册的路由应可匹配")
	}
	handlers := matched.Middlewares()
	if len(handlers) != 1 || reflect.ValueOf(handlers[0]).Pointer() != reflect.ValueOf(routeMiddleware).Pointer() {
		t.Fatalf("移除组中间件后的结果不正确: %#v", handlers)
	}

	if _, err = router.Get("/late", "Late@Index"); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("首次匹配冻结路由后必须拒绝继续注册，实际为 %v", err)
	}
}

func TestStaticRoutesKeepDomainVariantsAndPreferExactDomain(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/users", "Public@Index"); err != nil {
		t.Fatalf("注册公共路由失败: %v", err)
	}
	if err := router.Domain("api.example.com", func(group *Group) error {
		_, registerErr := group.Get("/users", "API@Index")
		return registerErr
	}); err != nil {
		t.Fatalf("注册 API 域名路由失败: %v", err)
	}
	if err := router.Domain("admin.example.com", func(group *Group) error {
		_, registerErr := group.Get("/users", "Admin@Index")
		return registerErr
	}); err != nil {
		t.Fatalf("注册管理域名路由失败: %v", err)
	}

	tests := map[string]string{
		"api.example.com":   "API@Index",
		"admin.example.com": "Admin@Index",
		"www.example.com":   "Public@Index",
	}
	for host, expected := range tests {
		req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://"+host+"/users", nil))
		matched, _ := matchForTest(t, router, req)
		if matched == nil || matched.Handler() != expected {
			t.Fatalf("Host %s 应匹配 %s，实际路由=%#v", host, expected, matched)
		}
	}
}

func TestRouterExtensionParticipatesInMatchingAndURLGeneration(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/reports/:name", "Report@Show")
	if err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	if err = registered.WithExtension("json"); err != nil {
		t.Fatalf("设置扩展名失败: %v", err)
	}
	if err = registered.WithName("report.show"); err != nil {
		t.Fatalf("设置名称失败: %v", err)
	}

	matched, params := matchForTest(t, router, fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "http://example.com/reports/a%2Fb.json", nil),
	))
	if matched == nil || params["name"] != "a/b" {
		t.Fatalf("带编码斜杠的扩展名路由匹配错误，路由=%#v 参数=%#v", matched, params)
	}
	withoutExtension, _, err := router.Match(fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodGet, "http://example.com/reports/a%2Fb", nil),
	))
	if err != nil {
		t.Fatalf("无扩展名请求不应产生配置错误: %v", err)
	}
	if withoutExtension != nil {
		t.Fatal("设置扩展名约束后，无扩展名请求不得匹配")
	}

	builtURL, err := router.URL("report.show", map[string]interface{}{"name": "a/b"})
	if err != nil {
		t.Fatalf("生成 URL 失败: %v", err)
	}
	if builtURL != "/reports/a%2Fb.json" {
		t.Fatalf("扩展名 URL 生成错误，实际为 %q", builtURL)
	}
}

// TestRouterThinkPHPStyleURLOptions 验证 route.json 的大小写、前缀匹配、后缀和斜杠规则真正参与运行时匹配。
func TestRouterThinkPHPStyleURLOptions(t *testing.T) {
	router := NewRouter()
	if err := router.SetCaseSensitive(false); err != nil {
		t.Fatalf("设置大小写规则失败: %v", err)
	}
	if err := router.SetCompleteMatch(false); err != nil {
		t.Fatalf("设置前缀匹配规则失败: %v", err)
	}
	if err := router.SetRemoveSlash(false); err != nil {
		t.Fatalf("设置末尾斜杠规则失败: %v", err)
	}
	if err := router.SetDefaultExtension("html"); err != nil {
		t.Fatalf("设置默认后缀失败: %v", err)
	}
	registered, err := router.Get("/Users", "User@Index")
	if err != nil {
		t.Fatalf("注册配置路由失败: %v", err)
	}
	if err = registered.WithName("users.index"); err != nil {
		t.Fatalf("设置配置路由名称失败: %v", err)
	}

	for _, path := range []string{"/users", "/users.html", "/users/42"} {
		matched, _, matchErr := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)))
		if matchErr != nil || matched == nil {
			t.Fatalf("配置路由应匹配 %q，route=%#v err=%v", path, matched, matchErr)
		}
	}
	trailing, _, err := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/", nil)))
	if err != nil {
		t.Fatalf("末尾斜杠匹配不应返回配置错误: %v", err)
	}
	if trailing != nil {
		t.Fatal("remove_slash=false 时，未声明斜杠的路由不应匹配带斜杠请求")
	}
	if built, err := router.URL("users.index", nil); err != nil || built != "/Users.html" {
		t.Fatalf("默认后缀应参与命名路由 URL 生成，url=%q err=%v", built, err)
	}
}

// TestRouterCompleteMatchControlsDynamicRouteSuffix 验证完整匹配开关同时作用于动态路由索引和最终校验。
func TestRouterCompleteMatchControlsDynamicRouteSuffix(t *testing.T) {
	prefixRouter := NewRouter()
	if err := prefixRouter.SetCompleteMatch(false); err != nil {
		t.Fatalf("设置前缀匹配规则失败: %v", err)
	}
	if _, err := prefixRouter.Get("/users/:id", "User@Show"); err != nil {
		t.Fatalf("注册动态路由失败: %v", err)
	}
	matched, params, err := prefixRouter.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/18/extra", nil)))
	if err != nil || matched == nil || params["id"] != "18" {
		t.Fatalf("非完整匹配应返回动态路由及参数，route=%#v params=%#v err=%v", matched, params, err)
	}

	completeRouter := NewRouter()
	if _, err := completeRouter.Get("/users/:id", "User@Show"); err != nil {
		t.Fatalf("注册完整匹配路由失败: %v", err)
	}
	matched, _, err = completeRouter.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/18/extra", nil)))
	if err != nil {
		t.Fatalf("完整匹配失败不应返回配置错误: %v", err)
	}
	if matched != nil {
		t.Fatal("complete_match=true 时不应接受动态路由后的额外路径")
	}
}

func TestRouterRejectsDuplicateAndInvalidDefinitions(t *testing.T) {
	router := NewRouter()
	first, err := router.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册首条路由失败: %v", err)
	}
	if err = first.WithName("users.show"); err != nil {
		t.Fatalf("设置首条名称失败: %v", err)
	}
	if _, err = router.Get("/users/:id", "Other@Show"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("重复方法、路径和域名必须被拒绝，实际为 %v", err)
	}
	second, err := router.Get("/profiles/:id", "Profile@Show")
	if err != nil {
		t.Fatalf("注册第二条路由失败: %v", err)
	}
	if err = second.WithName("users.show"); !errors.Is(err, ErrDuplicateRouteName) {
		t.Fatalf("重复路由名称必须被拒绝，实际为 %v", err)
	}
	if _, err = router.Get("/bad//path", "Bad@Index"); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("非规范路径必须被拒绝，实际为 %v", err)
	}
	if _, err = router.Get("/nil", nil); !errors.Is(err, ErrInvalidRouteHandler) {
		t.Fatalf("空处理器必须被拒绝，实际为 %v", err)
	}
	if _, err = router.Get("/nil-middleware", "Safe@Index", nil); !errors.Is(err, ErrInvalidRouteMiddleware) {
		t.Fatalf("空中间件必须被拒绝，实际为 %v", err)
	}
}

func TestRouterRejectsInvalidPatternsAndComplexOptionalPaths(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	if err = registered.WithPattern("id", "[0-9"); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("非法正则必须在配置阶段返回错误，实际为 %v", err)
	}

	segments := make([]string, 0, maxOptionalRouteSegments+1)
	for index := 0; index <= maxOptionalRouteSegments; index++ {
		segments = append(segments, ":p"+string(rune('a'+index))+"?")
	}
	if _, err = router.Get("/"+strings.Join(segments, "/"), "Too@Complex"); !errors.Is(err, ErrRouteTooComplex) {
		t.Fatalf("过多可选段必须被拒绝，实际为 %v", err)
	}
}

func TestRouterMethodSemantics(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/items", "Item@Index"); err != nil {
		t.Fatalf("注册 GET 路由失败: %v", err)
	}

	headRoute, _ := matchForTest(t, router, fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodHead, "http://example.com/items", nil),
	))
	if headRoute == nil || headRoute.Method() != http.MethodGet {
		t.Fatalf("HEAD 应回落到 GET 路由，实际为 %#v", headRoute)
	}

	_, _, err := router.Match(fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodPost, "http://example.com/items", nil),
	))
	var methodErr *MethodNotAllowedError
	if !errors.As(err, &methodErr) {
		t.Fatalf("方法不匹配应返回 MethodNotAllowedError，实际为 %v", err)
	}
	if strings.Join(methodErr.Allowed, ",") != "GET,HEAD,OPTIONS" {
		t.Fatalf("Allow 方法集合错误: %#v", methodErr.Allowed)
	}

	optionsRoute, _ := matchForTest(t, router, fwcontext.MustNewRequest(
		httptest.NewRequest(http.MethodOptions, "http://example.com/items", nil),
	))
	response := optionsRoute.Handler().(func(*fwcontext.Request) *fwcontext.Response)(nil)
	if response.GetStatus() != http.StatusNoContent || response.Headers().Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("自动 OPTIONS 响应错误: status=%d allow=%q", response.GetStatus(), response.Headers().Get("Allow"))
	}
}

func TestRouterURLRejectsUnsupportedValuesWithoutCallingStringer(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册路由失败: %v", err)
	}
	if err = registered.WithName("users.show"); err != nil {
		t.Fatalf("设置名称失败: %v", err)
	}
	if _, err = router.URL("users.show", map[string]interface{}{"id": routePanickingStringer{}}); !errors.Is(err, ErrInvalidRouteParameter) {
		t.Fatalf("复杂参数必须被拒绝且不能调用 String，实际为 %v", err)
	}
	if _, err = router.URL("users.show", map[string]interface{}{"id": math.NaN()}); !errors.Is(err, ErrInvalidRouteParameter) {
		t.Fatalf("NaN 参数必须被拒绝，实际为 %v", err)
	}
}

func TestResourceOnlyAndExcept(t *testing.T) {
	router := NewRouter()
	posts, err := router.Resource("/posts", "Post")
	if err != nil {
		t.Fatalf("注册 posts 资源失败: %v", err)
	}
	if err = posts.Only("index", "read"); err != nil {
		t.Fatalf("裁剪 posts 资源失败: %v", err)
	}
	comments, err := router.Resource("/comments", "Comment")
	if err != nil {
		t.Fatalf("注册 comments 资源失败: %v", err)
	}
	if err = comments.Except("edit"); err != nil {
		t.Fatalf("裁剪 comments 资源失败: %v", err)
	}

	if matched, _ := matchForTest(t, router, fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/posts", nil))); matched == nil {
		t.Fatal("Only 保留的资源路由应存在")
	}
	if matched, _, matchErr := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/posts", nil))); matched != nil || matchErr == nil {
		t.Fatal("Only 未保留的方法应返回方法不允许")
	}
	if matched, _, matchErr := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/comments/9/edit", nil))); matched != nil || matchErr != nil {
		t.Fatalf("Except 排除的唯一路径应表现为未找到，实际路由=%#v 错误=%v", matched, matchErr)
	}
}

func matchForTest(t *testing.T, router *Router, req *fwcontext.Request) (*Route, map[string]string) {
	t.Helper()
	matched, params, err := router.Match(req)
	if err != nil {
		t.Fatalf("匹配路由失败: %v", err)
	}
	return matched, params
}

type routePanickingStringer struct{}

func (routePanickingStringer) String() string {
	panic("路由参数不得调用不受信对象的 String 方法")
}
