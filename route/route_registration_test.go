package route

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestRouterRegistrationSurface 验证公开注册方法、嵌套作用域和快照使用同一套严格契约。
func TestRouterRegistrationSurface(t *testing.T) {
	router := NewRouter()
	err := router.Group("/api", func(api *Group) error {
		registrations := []struct {
			register func() (*Route, error)
		}{
			{register: func() (*Route, error) { return api.Put("/put/:id", "Item@Update") }},
			{register: func() (*Route, error) { return api.Delete("/delete/:id", "Item@Delete") }},
			{register: func() (*Route, error) { return api.Patch("/patch/:id", "Item@Patch") }},
			{register: func() (*Route, error) { return api.Options("/explicit-options", "Item@Options") }},
			{register: func() (*Route, error) { return api.Head("/explicit-head", "Item@Head") }},
			{register: func() (*Route, error) { return api.Any("/any", "Item@Any") }},
		}
		for _, registration := range registrations {
			if _, registerErr := registration.register(); registerErr != nil {
				return registerErr
			}
		}
		if _, ruleErr := api.Rule("/multi", "Item@Multi", "GET|POST"); ruleErr != nil {
			return ruleErr
		}
		return api.Group("/nested", func(nested *Group) error {
			return nested.Domain("nested.example.com", func(domain *Group) error {
				_, registerErr := domain.Get("/item", "Item@Nested")
				return registerErr
			})
		})
	})
	if err != nil {
		t.Fatalf("注册嵌套路由失败: %v", err)
	}
	redirect, err := router.Redirect("/old", "/new", http.StatusMovedPermanently)
	if err != nil {
		t.Fatalf("注册重定向失败: %v", err)
	}
	domainRoute, err := router.Get("/domain", "Domain@Index")
	if err != nil {
		t.Fatalf("注册单路由域名失败: %v", err)
	}
	if err = domainRoute.WithDomain("API.EXAMPLE.COM."); err != nil {
		t.Fatalf("设置单路由域名失败: %v", err)
	}

	tests := []struct {
		method string
		url    string
	}{
		{method: http.MethodPut, url: "http://example.com/api/put/1"},
		{method: http.MethodDelete, url: "http://example.com/api/delete/1"},
		{method: http.MethodPatch, url: "http://example.com/api/patch/1"},
		{method: http.MethodOptions, url: "http://example.com/api/explicit-options"},
		{method: http.MethodHead, url: "http://example.com/api/explicit-head"},
		{method: http.MethodTrace, url: "http://example.com/api/any"},
		{method: http.MethodPost, url: "http://example.com/api/multi"},
		{method: http.MethodGet, url: "http://nested.example.com/api/nested/item"},
		{method: http.MethodGet, url: "http://api.example.com/domain"},
	}
	for _, test := range tests {
		matched, _, matchErr := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(test.method, test.url, nil)))
		if matchErr != nil || matched == nil {
			t.Fatalf("%s %s 匹配失败，路由=%#v 错误=%v", test.method, test.url, matched, matchErr)
		}
		if matched.Path() == "" || matched.Handler() == nil {
			t.Fatalf("路由只读访问器返回空数据: %#v", matched)
		}
	}

	redirectResponse := redirect.Handler().(func(*fwcontext.Request) *fwcontext.Response)(nil)
	if redirectResponse.GetStatus() != http.StatusMovedPermanently {
		t.Fatalf("重定向状态码错误: %d", redirectResponse.GetStatus())
	}
	routes, err := router.Routes()
	if err != nil || len(routes) != 11 {
		t.Fatalf("路由快照错误，数量=%d 错误=%v", len(routes), err)
	}
}

// TestRouterRegistrationValidation 验证注册错误在冻结前返回且不会留下部分路由。
func TestRouterRegistrationValidation(t *testing.T) {
	router := NewRouter()
	if err := router.SetDefaultController("123Bad"); err == nil {
		t.Fatal("非法默认控制器必须被拒绝")
	}
	if err := router.SetDefaultAction("bad-action"); err == nil {
		t.Fatal("非法默认动作必须被拒绝")
	}
	if err := router.Group("/api", nil); err == nil {
		t.Fatal("空分组回调必须被拒绝")
	}
	if err := router.Domain("https://example.com", func(*Group) error { return nil }); !errors.Is(err, ErrInvalidRouteDomain) {
		t.Fatalf("带协议的域名必须被拒绝，实际为 %v", err)
	}
	if _, err := router.Rule("/empty", "Item@Index", "GET||POST"); err == nil {
		t.Fatal("包含空方法的 Rule 必须原子失败")
	}
	if _, err := router.Rule("/duplicate", "Item@Index", "GET|GET"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("Rule 重复方法必须返回 ErrDuplicateRoute，实际为 %v", err)
	}
	if _, err := router.Redirect("/bad", "/target", 200); err == nil {
		t.Fatal("非 3xx 重定向状态必须被拒绝")
	}
	for _, status := range []int{http.StatusMultipleChoices, http.StatusNotModified, http.StatusUseProxy, 306} {
		if _, err := router.Redirect("/bad", "/target", status); err == nil {
			t.Fatalf("不具备重定向语义的状态码 %d 必须被拒绝", status)
		}
	}
	if _, err := router.Redirect("/bad", "/target", 301, 302); err == nil {
		t.Fatal("多个重定向状态码必须被拒绝")
	}
	if _, err := router.Redirect("/bad", "/target\r\nX-Test: value"); err == nil {
		t.Fatal("包含换行的重定向目标必须被拒绝")
	}

	registered, err := router.Get("/users/:id", "User@Show")
	if err != nil {
		t.Fatalf("注册基础路由失败: %v", err)
	}
	if err = registered.WithName("bad name"); err == nil {
		t.Fatal("非法路由名称必须被拒绝")
	}
	if err = registered.WithPattern("missing", `[0-9]+`); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("不存在的参数约束必须被拒绝，实际为 %v", err)
	}
	if err = registered.WithPattern("id", strings.Repeat("a", maxRoutePatternLength+1)); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("过长正则必须被拒绝，实际为 %v", err)
	}
	if err = registered.WithExtension("bad/ext"); err == nil {
		t.Fatal("非法扩展名必须被拒绝")
	}
	if err = registered.WithDomain(""); !errors.Is(err, ErrInvalidRouteDomain) {
		t.Fatalf("空单路由域名必须被拒绝，实际为 %v", err)
	}

	resource, err := router.Resource("/posts", "Post")
	if err != nil {
		t.Fatalf("注册资源路由失败: %v", err)
	}
	if err = resource.Only("unknown"); !errors.Is(err, ErrInvalidResourceAction) {
		t.Fatalf("未知 Only 动作必须被拒绝，实际为 %v", err)
	}
	if err = resource.Except("unknown"); !errors.Is(err, ErrInvalidResourceAction) {
		t.Fatalf("未知 Except 动作必须被拒绝，实际为 %v", err)
	}
	if _, err = router.Miss("Error@NotFound"); err != nil {
		t.Fatalf("注册 Miss 失败: %v", err)
	}
	if _, err = router.Miss("Other@NotFound"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("重复 Miss 必须被拒绝，实际为 %v", err)
	}
}

// TestRouteMutatorsRejectZeroValues 验证配置 API 对空接收者安全失败，避免启动错误退化为 panic。
func TestRouteMutatorsRejectZeroValues(t *testing.T) {
	var nilRoute *Route
	checks := []func() error{
		func() error { return nilRoute.WithName("users.show") },
		func() error { return nilRoute.WithPattern("id", `[0-9]+`) },
		func() error { return nilRoute.WithDomain("example.com") },
		func() error { return nilRoute.WithExtension("json") },
		func() error { return nilRoute.WithoutMiddleware() },
	}
	for index, check := range checks {
		if err := check(); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("第 %d 个空路由配置应返回 ErrInvalidRoute，实际为 %v", index+1, err)
		}
	}

	zeroRoute := &Route{}
	if err := zeroRoute.WithName("users.show"); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("零值路由配置应返回 ErrInvalidRoute，实际为 %v", err)
	}
	var nilResource *ResourceRoute
	if err := nilResource.Only("index"); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("空资源路由裁剪应返回 ErrInvalidRoute，实际为 %v", err)
	}
	if err := nilResource.Except("index"); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("空资源路由排除应返回 ErrInvalidRoute，实际为 %v", err)
	}
}

// TestRouterFreezeKeepsFailure 验证索引构建失败后不会在后续调用中被误报为成功。
func TestRouterFreezeKeepsFailure(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users", "User@Index")
	if err != nil {
		t.Fatalf("注册测试路由失败: %v", err)
	}
	// 模拟内部不变量被破坏，确保冻结错误可以稳定传播给所有后续调用。
	registered.path = "/users//broken"
	firstErr := router.Freeze()
	secondErr := router.Freeze()
	if !errors.Is(firstErr, ErrInvalidRoute) || !errors.Is(secondErr, ErrInvalidRoute) {
		t.Fatalf("冻结错误必须稳定返回，首次=%v，再次=%v", firstErr, secondErr)
	}
}
