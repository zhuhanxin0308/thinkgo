package route

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// TestIssue580DefaultRouterResourceAndLayeredControllers 验证默认 completeMatch=false 下
// Resource 的全部 7 个 REST 动作均不会被浅层静态前缀劫持，且支持分层控制器与尾斜杠路径。
func TestIssue580DefaultRouterResourceAndLayeredControllers(t *testing.T) {
	router := NewRouter()
	if _, err := router.Resource("/posts", "Post"); err != nil {
		t.Fatalf("注册 Post 资源失败: %v", err)
	}
	if _, err := router.Resource("/items/", "api.Item"); err != nil {
		t.Fatalf("注册点分层控制器资源失败: %v", err)
	}
	if _, err := router.Resource("/orders", "api/Order"); err != nil {
		t.Fatalf("注册斜杠分层控制器资源失败: %v", err)
	}

	cases := []struct {
		method     string
		path       string
		wantHandle string
		wantParams map[string]string
	}{
		{http.MethodGet, "/posts", "Post/index", nil},
		{http.MethodGet, "/posts/create", "Post/create", nil},
		{http.MethodPost, "/posts", "Post/save", nil},
		{http.MethodGet, "/posts/42", "Post/read", map[string]string{"id": "42"}},
		{http.MethodGet, "/posts/42/edit", "Post/edit", map[string]string{"id": "42"}},
		{http.MethodPut, "/posts/42", "Post/update", map[string]string{"id": "42"}},
		{http.MethodDelete, "/posts/42", "Post/delete", map[string]string{"id": "42"}},

		{http.MethodGet, "/items", "api/Item/index", nil},
		{http.MethodGet, "/items/create", "api/Item/create", nil},
		{http.MethodPost, "/items", "api/Item/save", nil},
		{http.MethodGet, "/items/7", "api/Item/read", map[string]string{"id": "7"}},
		{http.MethodGet, "/items/7/edit", "api/Item/edit", map[string]string{"id": "7"}},
		{http.MethodPut, "/items/7", "api/Item/update", map[string]string{"id": "7"}},
		{http.MethodDelete, "/items/7", "api/Item/delete", map[string]string{"id": "7"}},

		{http.MethodGet, "/orders/9/edit", "api/Order/edit", map[string]string{"id": "9"}},
	}
	for _, tc := range cases {
		req := fwcontext.MustNewRequest(httptest.NewRequest(tc.method, "http://example.com"+tc.path, nil))
		matched, params, err := router.Match(req)
		if err != nil || matched == nil {
			t.Fatalf("%s %s 未命中: matched=%#v err=%v", tc.method, tc.path, matched, err)
		}
		if matched.Handler() != tc.wantHandle {
			t.Fatalf("%s %s 处理器错误: got=%v want=%s", tc.method, tc.path, matched.Handler(), tc.wantHandle)
		}
		if !reflect.DeepEqual(params, tc.wantParams) {
			t.Fatalf("%s %s 参数错误: got=%#v want=%#v", tc.method, tc.path, params, tc.wantParams)
		}
	}

	for _, badController := range []string{"", ".", "/", "/api/Item", "api/Item/", ".Item", "Item.", "api..Item", "api//Item", "123Bad", "Post@index"} {
		if _, err := NewRouter().Resource("/x", badController); !errors.Is(err, ErrInvalidRouteHandler) {
			t.Fatalf("非法资源控制器 %q 应返回 ErrInvalidRouteHandler，实际为 %v", badController, err)
		}
	}
	if _, err := NewRouter().Resource("/items//sub", "api.Item"); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("包含双斜杠的资源路径应返回 ErrInvalidRoute，实际为 %v", err)
	}
}

// TestIssue580ShallowStaticDoesNotHijackProtectedDeepDynamic 验证默认非完整匹配下，
// 浅层公开静态探针路由不会抢占深层受保护动态路由（含完整匹配及更深前缀匹配）。
func TestIssue580ShallowStaticDoesNotHijackProtectedDeepDynamic(t *testing.T) {
	router := NewRouter()
	var authCount int
	authMw := func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		authCount++
		if req.Raw().Header.Get("Authorization") != "Bearer secret" {
			return fwcontext.NewResponse().Code(http.StatusUnauthorized)
		}
		return next(req)
	}
	if _, err := router.Get("/api", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("public-probe")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/api/tenants/:id/secrets", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("tenant-secret")
	}, authMw); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/tenants/1/secrets", "/api/tenants/1/secrets/extra"} {
		authCount = 0
		unauthReq := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil))
		matched, params, err := router.Match(unauthReq)
		if err != nil || matched == nil || params["id"] != "1" {
			t.Fatalf("路径 %s 未命中深层动态路由: matched=%#v params=%#v err=%v", path, matched, params, err)
		}
		resp := matched.ExecuteMiddleware(unauthReq, matched.Handler().(func(*fwcontext.Request) *fwcontext.Response))
		if resp.GetStatus() != http.StatusUnauthorized || authCount != 1 {
			t.Fatalf("路径 %s 鉴权被浅层静态路由绕过: status=%d authCount=%d body=%q", path, resp.GetStatus(), authCount, resp.GetBody())
		}
	}
}

// TestIssue340CaseFoldedDuplicateRoutesRejected 验证大小写不敏感配置下折叠等价的路由重复注册返回 ErrDuplicateRoute。
func TestIssue340CaseFoldedDuplicateRoutesRejected(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/Admin", "Public/index"); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/admin", "Admin/index"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("大小写折叠等价静态路由应返回 ErrDuplicateRoute，实际为 %v", err)
	}
	if _, err := router.Get("/Users/:id", "User/read"); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/users/:id", "User/other"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("大小写折叠等价动态字面量段应返回 ErrDuplicateRoute，实际为 %v", err)
	}
	// 不同参数名的同结构动态路由仍保持兼容可注册
	if _, err := router.Get("/users/:slug", "User/bySlug"); err != nil {
		t.Fatalf("不同参数名的路由不应误判重复: %v", err)
	}

	// SetCaseSensitive(true) 下允许同时存在，切回 false 时必须拒绝冲突
	csRouter := NewRouter()
	if err := csRouter.SetCaseSensitive(true); err != nil {
		t.Fatal(err)
	}
	if _, err := csRouter.Get("/Admin", "Admin/upper"); err != nil {
		t.Fatal(err)
	}
	if _, err := csRouter.Get("/admin", "Admin/lower"); err != nil {
		t.Fatal(err)
	}
	if err := csRouter.SetCaseSensitive(false); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("存在大小写冲突路由时关闭大小写敏感应返回 ErrDuplicateRoute，实际为 %v", err)
	}
}

// TestIssue351DecodedSlashAndControlCharactersRejected 验证解码后出现斜线或 C0/DEL 控制字符的路径段被拒绝。
func TestIssue351DecodedSlashAndControlCharactersRejected(t *testing.T) {
	router := NewRouter()
	fileRoute, err := router.Get("/files/:name", "File/read")
	if err != nil {
		t.Fatal(err)
	}
	if err := fileRoute.WithPattern("name", `.+`); err != nil {
		t.Fatal(err)
	}
	if err := fileRoute.WithName("files.read"); err != nil {
		t.Fatal(err)
	}

	maliciousPaths := []string{
		"/files/a%2F..%2F..%2Fetc%2Fpasswd",
		"/files/a%2fb",
		"/files/a%5cb",
		"/files/a%00b",
		"/files/a%09b",
		"/files/a%0ab",
		"/files/a%0db",
		"/files/a%1fb",
		"/files/a%7fb",
	}
	for _, rawPath := range maliciousPaths {
		req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+rawPath, nil))
		matched, params, matchErr := router.Match(req)
		if !errors.Is(matchErr, ErrInvalidRequestPath) || matched != nil || params != nil {
			t.Fatalf("路径 %q 应被拒绝为 ErrInvalidRequestPath: matched=%#v params=%#v err=%v", rawPath, matched, params, matchErr)
		}
	}

	for _, invalidParam := range []string{"a/../../etc/passwd", "a/b", "a\tb", "a\x1fb", "a\x7fb"} {
		if built, urlErr := router.URL("files.read", map[string]interface{}{"name": invalidParam}); !errors.Is(urlErr, ErrInvalidRouteParameter) || built != "" {
			t.Fatalf("危险参数 %q 生成 URL 应返回 ErrInvalidRouteParameter: built=%q err=%v", invalidParam, built, urlErr)
		}
	}
}

// TestIssue570GroupModifiersAndRetrofittingUnit 验证底层 Group 前置派生、后置批量回填、冲突原子性与作用域 Miss。
func TestIssue570GroupModifiersAndRetrofittingUnit(t *testing.T) {
	var trace []string
	marker := func(label string) middleware.Handler {
		return func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			trace = append(trace, label)
			return next(req)
		}
	}
	handler := func(label string) func(*fwcontext.Request) *fwcontext.Response {
		return func(*fwcontext.Request) *fwcontext.Response {
			trace = append(trace, label)
			return fwcontext.NewResponse().Content(label)
		}
	}

	// 1. 前置派生修饰链：RootGroup -> WithMiddleware -> WithNamedMiddleware -> WithoutNamedMiddleware -> WithDomain -> WithExtension -> WithPatterns
	prefixRouter := NewRouter()
	if (*Router)(nil).RootGroup() != nil {
		t.Fatal("nil Router.RootGroup 应返回 nil")
	}
	root := prefixRouter.RootGroup()
	withAnon, err := root.WithMiddleware(marker("m1"))
	if err != nil {
		t.Fatal(err)
	}
	withNamed, err := withAnon.WithNamedMiddleware("temp", marker("temp"))
	if err != nil {
		t.Fatal(err)
	}
	withoutNamed, err := withNamed.WithoutNamedMiddleware("temp")
	if err != nil {
		t.Fatal(err)
	}
	if noop, noopErr := withoutNamed.WithoutMiddleware(); noopErr != nil || noop == nil {
		t.Fatalf("空参 WithoutMiddleware 应返回克隆组: %v", noopErr)
	}
	if _, badErr := withoutNamed.WithoutMiddleware(marker("x")); !errors.Is(badErr, ErrInvalidRouteMiddleware) {
		t.Fatalf("非空 WithoutMiddleware 应返回 ErrInvalidRouteMiddleware: %v", badErr)
	}
	scoped, err := withoutNamed.WithDomain("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	scoped, err = scoped.WithExtension("json")
	if err != nil {
		t.Fatal(err)
	}
	scoped, err = scoped.WithPatterns(map[string]string{"id": `\d+`})
	if err != nil {
		t.Fatal(err)
	}
	err = scoped.Group("/v1", func(v1 *Group) error {
		if _, err := v1.Get("/users/:id", handler("user"), marker("route")); err != nil {
			return err
		}
		res, err := v1.Resource("/posts", "Post")
		if err != nil {
			return err
		}
		if len(res.Routes()) != 7 {
			t.Fatalf("ResourceRoute.Routes 数量错误: %d", len(res.Routes()))
		}
		if err := res.Only("index", "read"); err != nil {
			return err
		}
		if len(res.Routes()) != 2 {
			t.Fatalf("Only 后 ResourceRoute.Routes 数量错误: %d", len(res.Routes()))
		}
		_, err = v1.SetMiss(anyMethod, handler("v1_miss"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	trace = trace[:0]
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://api.example.com/v1/users/12.json", nil))
	matched, params, err := prefixRouter.Match(req)
	if err != nil || matched == nil || params["id"] != "12" {
		t.Fatalf("前置派生路由匹配失败: matched=%#v params=%#v err=%v", matched, params, err)
	}
	_ = matched.ExecuteMiddleware(req, matched.Handler().(func(*fwcontext.Request) *fwcontext.Response))
	if !reflect.DeepEqual(trace, []string{"m1", "route", "user"}) {
		t.Fatalf("前置派生中间件顺序错误: %v", trace)
	}

	// 验证分组作用域 Miss 继承 Domain + Ext + 前缀 + 中间件
	trace = trace[:0]
	missReq := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://api.example.com/v1/unknown.json", nil))
	matchedMiss, _, err := prefixRouter.Match(missReq)
	if err != nil || matchedMiss == nil {
		t.Fatalf("分组作用域 Miss 未命中: matched=%#v err=%v", matchedMiss, err)
	}
	_ = matchedMiss.ExecuteMiddleware(missReq, matchedMiss.Handler().(func(*fwcontext.Request) *fwcontext.Response))
	if !reflect.DeepEqual(trace, []string{"m1", "v1_miss"}) {
		t.Fatalf("分组作用域 Miss 执行顺序错误: %v", trace)
	}
	if m, _, err := prefixRouter.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://api.example.com/v1/unknown", nil))); err != nil || m != nil {
		t.Fatalf("缺失强制后缀的请求不应命中带后缀分组 Miss: %#v %v", m, err)
	}

	// 2. 后置批量回填：ApplyMiddlewareToRoutes / ApplyNamedMiddlewareToRoutes / ApplyWithoutNamedMiddlewareToRoutes / ApplyDomainToRoutes / ApplyExtensionToRoutes / ApplyPatternsToRoutes
	retroRouter := NewRouter()
	clashExt, err := retroRouter.Get("/clash-ext", handler("clash_ext"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clashExt.WithExtension("xml"); err != nil {
		t.Fatal(err)
	}
	if err := retroRouter.Domain("dup.example.com", func(d *Group) error {
		_, err := d.Get("/clash2", handler("p2"))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var collected []*Route
	var capturedGroup *Group
	err = retroRouter.Group("/admin", func(g *Group) error {
		capturedGroup = g
		r1, err := g.Get("/items/:id", handler("items"), marker("route_tail"))
		if err != nil {
			return err
		}
		r2, err := g.Get("/override/:id", handler("override"))
		if err != nil {
			return err
		}
		if err := r2.WithDomain("override.example.com"); err != nil {
			return err
		}
		if err := r2.WithExtension("yaml"); err != nil {
			return err
		}
		if err := r2.WithPattern("id", `[a-z]+`); err != nil {
			return err
		}
		miss, err := g.SetMiss(anyMethod, handler("admin_miss"))
		if err != nil {
			return err
		}
		collected = append(collected, r1, r1, nil, r2, miss)
		return nil
	}, marker("inline"))
	if err != nil {
		t.Fatal(err)
	}

	updated, err := capturedGroup.ApplyMiddlewareToRoutes(collected, marker("post_m1"))
	if err != nil {
		t.Fatal(err)
	}
	updated, err = updated.ApplyMiddlewareToRoutes(collected)
	if err != nil {
		t.Fatal(err)
	}
	updated, err = updated.ApplyNamedMiddlewareToRoutes(collected, "auth", marker("post_auth"))
	if err != nil {
		t.Fatal(err)
	}
	if _, dupErr := updated.ApplyNamedMiddlewareToRoutes(collected, "auth", marker("dup")); !errors.Is(dupErr, ErrInvalidRouteMiddleware) {
		t.Fatalf("重复命名中间件必须失败: %v", dupErr)
	}
	updated, err = updated.ApplyNamedMiddlewareToRoutes(collected, "removable", marker("removable"))
	if err != nil {
		t.Fatal(err)
	}
	updated, err = updated.ApplyWithoutNamedMiddlewareToRoutes(collected, "removable")
	if err != nil {
		t.Fatal(err)
	}
	updated, err = updated.ApplyDomainToRoutes(collected, "admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	updated, err = updated.ApplyExtensionToRoutes(collected, "json")
	if err != nil {
		t.Fatal(err)
	}
	updated, err = updated.ApplyPatternsToRoutes(collected, map[string]string{"id": `\d+`})
	if err != nil {
		t.Fatal(err)
	}
	_ = updated

	// 验证域名/后缀批量冲突原子拒绝
	rootGroup := retroRouter.RootGroup()
	rootClash, err := retroRouter.Get("/clash-ext", handler("plain"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootGroup.ApplyExtensionToRoutes([]*Route{rootClash}, "xml"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("批量扩展名冲突应返回 ErrDuplicateRoute，实际为 %v", err)
	}
	publicClash, err := retroRouter.Get("/clash2", handler("p1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootGroup.ApplyDomainToRoutes([]*Route{publicClash}, "dup.example.com"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("批量域名冲突应返回 ErrDuplicateRoute，实际为 %v", err)
	}

	// 验证回填结果：inline -> post_m1 -> post_auth -> route_tail -> items
	trace = trace[:0]
	reqItems := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://admin.example.com/admin/items/88.json", nil))
	matchedItems, itemParams, err := retroRouter.Match(reqItems)
	if err != nil || matchedItems == nil || itemParams["id"] != "88" {
		t.Fatalf("后置回填路由匹配失败: matched=%#v params=%#v err=%v", matchedItems, itemParams, err)
	}
	_ = matchedItems.ExecuteMiddleware(reqItems, matchedItems.Handler().(func(*fwcontext.Request) *fwcontext.Response))
	if !reflect.DeepEqual(trace, []string{"inline", "post_m1", "post_auth", "route_tail", "items"}) {
		t.Fatalf("后置回填中间件顺序错误: %v", trace)
	}

	// 验证路由显式覆盖的 Domain / Ext / Pattern 不被组级回填覆盖
	reqOverride := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://override.example.com/admin/override/abc.yaml", nil))
	matchedOverride, overrideParams, err := retroRouter.Match(reqOverride)
	if err != nil || matchedOverride == nil || overrideParams["id"] != "abc" {
		t.Fatalf("显式覆盖路由匹配失败: matched=%#v params=%#v err=%v", matchedOverride, overrideParams, err)
	}

	// 3. 验证空接收者、非法参数与冻结后拒绝修改
	var nilGroup *Group
	var nilResource *ResourceRoute
	if nilResource.Routes() != nil {
		t.Fatal("nil ResourceRoute.Routes 应返回 nil")
	}
	for index, check := range []func() error{
		func() error { _, e := nilGroup.WithMiddleware(); return e },
		func() error { _, e := nilGroup.WithoutNamedMiddleware("x"); return e },
		func() error { _, e := nilGroup.WithoutMiddleware(); return e },
		func() error { _, e := nilGroup.WithDomain("a.com"); return e },
		func() error { _, e := nilGroup.WithExtension("json"); return e },
		func() error { _, e := nilGroup.WithPatterns(nil); return e },
		func() error { _, e := nilGroup.ApplyMiddlewareToRoutes(nil); return e },
		func() error { _, e := nilGroup.ApplyNamedMiddlewareToRoutes(nil, "a", marker("a")); return e },
		func() error { _, e := nilGroup.ApplyWithoutNamedMiddlewareToRoutes(nil, "a"); return e },
		func() error { _, e := nilGroup.ApplyDomainToRoutes(nil, "a.com"); return e },
		func() error { _, e := nilGroup.ApplyExtensionToRoutes(nil, "json"); return e },
		func() error { _, e := nilGroup.ApplyPatternsToRoutes(nil, nil); return e },
		func() error { _, e := nilGroup.SetMiss(anyMethod, handler("m")); return e },
		func() error { _, e := (*Router)(nil).SetMiss(anyMethod, handler("m")); return e },
	} {
		if err := check(); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("第 %d 项空接收者应返回 ErrInvalidRoute，实际为 %v", index+1, err)
		}
	}

	frozenGroup := retroRouter.RootGroup()
	for index, check := range []func() error{
		func() error { _, e := frozenGroup.WithMiddleware(marker("x")); return e },
		func() error { _, e := frozenGroup.WithoutNamedMiddleware("x"); return e },
		func() error { _, e := frozenGroup.WithoutMiddleware(marker("x")); return e },
		func() error { _, e := frozenGroup.WithDomain("a.com"); return e },
		func() error { _, e := frozenGroup.WithExtension("json"); return e },
		func() error { _, e := frozenGroup.WithPatterns(map[string]string{"id": `\d+`}); return e },
		func() error { _, e := frozenGroup.ApplyMiddlewareToRoutes(nil, marker("x")); return e },
		func() error { _, e := frozenGroup.ApplyNamedMiddlewareToRoutes(nil, "a", marker("a")); return e },
		func() error { _, e := frozenGroup.ApplyWithoutNamedMiddlewareToRoutes(nil, "a"); return e },
		func() error { _, e := frozenGroup.ApplyDomainToRoutes(nil, "a.com"); return e },
		func() error { _, e := frozenGroup.ApplyExtensionToRoutes(nil, "json"); return e },
		func() error { _, e := frozenGroup.ApplyPatternsToRoutes(nil, map[string]string{"id": `\d+`}); return e },
	} {
		if err := check(); !errors.Is(err, ErrRouterFrozen) {
			t.Fatalf("第 %d 项冻结后调用应返回 ErrRouterFrozen，实际为 %v", index+1, err)
		}
	}

	freshGroup := NewRouter().RootGroup()
	if _, err := freshGroup.WithDomain(""); !errors.Is(err, ErrInvalidRouteDomain) {
		t.Fatalf("空分组域名应拒绝: %v", err)
	}
	if _, err := freshGroup.WithPatterns(map[string]string{"bad-name": `\d+`}); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("非法分组变量名应拒绝: %v", err)
	}
	if _, err := freshGroup.WithPatterns(map[string]string{"id": `[`}); !errors.Is(err, ErrInvalidRoutePattern) {
		t.Fatalf("非法分组变量正则应拒绝: %v", err)
	}
}
