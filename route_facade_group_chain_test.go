package framework

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	fwroute "github.com/zhuhanxin0308/thinkgo/v3/route"
)

func TestRouteFacadeGroupPostfixModifiersMatchPrefixSemantics(t *testing.T) {
	newMarker := func(tag string, trace *[]string) middleware.Handler {
		return func(req *Request, next func(*Request) *Response) *Response {
			*trace = append(*trace, tag)
			return next(req)
		}
	}

	buildApp := func(t *testing.T, postfix bool, trace *[]string) *App {
		t.Helper()
		app := NewConsoleAppUninitialized(t.TempDir())
		t.Cleanup(func() { _ = app.Close() })

		rootMw := newMarker("root", trace)
		adminM1 := newMarker("admin_m1", trace)
		adminM2 := newMarker("admin_m2", trace)
		v1Mw := newMarker("v1", trace)
		routeMw := newMarker("route", trace)
		handler := func(*Request) *Response {
			*trace = append(*trace, "handler")
			return NewResponse().Content("secret")
		}

		routes := app.Route()
		if postfix {
			routes.Group("/root", func() {
				routes.Group("/admin", func() {
					routes.Get("/secret", handler, routeMw)
					routes.Group("/v1", func() {
						routes.Get("/items/:id", handler, routeMw)
					}).NamedMiddleware("v1_mw", v1Mw)
				}).Middleware(adminM1).NamedMiddleware("auth", adminM2).Where(map[string]string{"id": `\d+`})
			}).Middleware(rootMw)
		} else {
			routes.Middleware(rootMw).Group("/root", func() {
				routes.Middleware(adminM1).NamedMiddleware("auth", adminM2).Where(map[string]string{"id": `\d+`}).Group("/admin", func() {
					routes.Get("/secret", handler, routeMw)
					routes.NamedMiddleware("v1_mw", v1Mw).Group("/v1", func() {
						routes.Get("/items/:id", handler, routeMw)
					})
				})
			})
		}
		return app
	}

	var postfixTrace, prefixTrace []string
	postfixApp := buildApp(t, true, &postfixTrace)
	prefixApp := buildApp(t, false, &prefixTrace)

	for _, path := range []string{"/root/admin/secret", "/root/admin/v1/items/42"} {
		postfixTrace = postfixTrace[:0]
		prefixTrace = prefixTrace[:0]

		req1 := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		matched1, params1, err1 := postfixApp.Route().Match(req1)
		if err1 != nil || matched1 == nil || !matched1.HasMiddleware() {
			t.Fatalf("后置链式分组匹配失败 path=%s: route=%#v err=%v", path, matched1, err1)
		}
		resp1 := matched1.ExecuteMiddleware(req1, matched1.Handler().(func(*Request) *Response))

		req2 := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		matched2, params2, err2 := prefixApp.Route().Match(req2)
		if err2 != nil || matched2 == nil || !matched2.HasMiddleware() {
			t.Fatalf("前置链式分组匹配失败 path=%s: route=%#v err=%v", path, matched2, err2)
		}
		resp2 := matched2.ExecuteMiddleware(req2, matched2.Handler().(func(*Request) *Response))

		if !reflect.DeepEqual(params1, params2) {
			t.Fatalf("参数不一致 path=%s: postfix=%#v prefix=%#v", path, params1, params2)
		}
		if string(resp1.GetBody()) != "secret" || string(resp2.GetBody()) != "secret" {
			t.Fatalf("响应体错误: postfix=%q prefix=%q", resp1.GetBody(), resp2.GetBody())
		}
		if !reflect.DeepEqual(postfixTrace, prefixTrace) {
			t.Fatalf("中间件执行顺序不一致 path=%s: postfix=%v prefix=%v", path, postfixTrace, prefixTrace)
		}
	}

	// 验证 Where 约束对组内动态路由生效，同时不影响无 :id 的静态路由。
	badReq := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://localhost/root/admin/v1/items/abc", nil))
	if matched, _, err := postfixApp.Route().Match(badReq); err != nil || matched != nil {
		t.Fatalf("后置 Where 数字约束应拒绝非数字参数: matched=%#v err=%v", matched, err)
	}
}

func TestRouteFacadeGroupPostfixDomainExtWithoutMiddlewareAndContinuation(t *testing.T) {
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	var calls []string
	authMw := middleware.Handler(func(req *Request, next func(*Request) *Response) *Response {
		calls = append(calls, "auth")
		return next(req)
	})
	handler := func(label string) func(*Request) *Response {
		return func(*Request) *Response {
			calls = append(calls, label)
			return NewResponse().Content(label)
		}
	}

	routes := app.Route()
	adminGroup := routes.Group("/admin", func() {
		routes.Get("/users/:id", handler("users"))
		routes.Get("/custom", handler("custom")).Domain("custom.example.com").Ext("xml")
		routes.Resource("/posts", "Post").Only("index", "read")
		routes.Group("/public", func() {
			routes.Get("/ping", handler("ping"))
		}).NamedMiddleware("temp", authMw).WithoutNamedMiddleware("temp")
	}).NamedMiddleware("auth", authMw).Domain("admin.example.com").Ext("json").Where(map[string]string{"id": `\d+`})

	// 继续在链尾派生子组，验证已回填修饰继续向后续子组传播。
	adminGroup.Group("/audit", func() {
		routes.Get("/logs/:id", handler("logs"))
	})

	// 1. 组内默认继承后置 Domain + Ext + Where + NamedMiddleware
	calls = calls[:0]
	reqUsers := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://admin.example.com/admin/users/7.json", nil))
	matched, params, err := routes.Match(reqUsers)
	if err != nil || matched == nil || params["id"] != "7" {
		t.Fatalf("匹配 /admin/users/7.json 失败: matched=%#v params=%#v err=%v", matched, params, err)
	}
	_ = matched.ExecuteMiddleware(reqUsers, matched.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(calls, []string{"auth", "users"}) {
		t.Fatalf("组中间件未生效: %v", calls)
	}

	// 错误域名或缺失强制后缀不得命中
	for _, rawURL := range []string{
		"http://other.example.com/admin/users/7.json",
		"http://admin.example.com/admin/users/7",
		"http://admin.example.com/admin/users/bad.json",
	} {
		if m, _, matchErr := routes.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, rawURL, nil))); matchErr != nil || m != nil {
			t.Fatalf("不应命中 %s: matched=%#v err=%v", rawURL, m, matchErr)
		}
	}

	// 2. 路由显式覆盖的 Domain/Ext 保持自身设置，同时仍获得外层后置鉴权中间件
	calls = calls[:0]
	reqCustom := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://custom.example.com/admin/custom.xml", nil))
	matchedCustom, _, err := routes.Match(reqCustom)
	if err != nil || matchedCustom == nil {
		t.Fatalf("显式覆盖 Domain/Ext 的路由未命中: matched=%#v err=%v", matchedCustom, err)
	}
	_ = matchedCustom.ExecuteMiddleware(reqCustom, matchedCustom.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(calls, []string{"auth", "custom"}) {
		t.Fatalf("显式覆盖路由中间件顺序错误: %v", calls)
	}

	// 3. 组内 Resource.Only 路由同样回填后置鉴权中间件与 Domain/Ext/Where
	reqPostRead := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://admin.example.com/admin/posts/99.json", nil))
	matchedPost, postParams, err := routes.Match(reqPostRead)
	if err != nil || matchedPost == nil || matchedPost.Handler() != "Post/read" || postParams["id"] != "99" || !matchedPost.HasMiddleware() {
		t.Fatalf("组内 Resource 路由后置修饰失败: matched=%#v params=%#v err=%v", matchedPost, postParams, err)
	}

	// 4. 后续链式派生的子组继承全部修饰
	calls = calls[:0]
	reqLogs := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://admin.example.com/admin/audit/logs/12.json", nil))
	matchedLogs, logParams, err := routes.Match(reqLogs)
	if err != nil || matchedLogs == nil || logParams["id"] != "12" {
		t.Fatalf("后续派生子组匹配失败: matched=%#v params=%#v err=%v", matchedLogs, logParams, err)
	}
	_ = matchedLogs.ExecuteMiddleware(reqLogs, matchedLogs.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(calls, []string{"auth", "logs"}) {
		t.Fatalf("后续派生子组中间件未继承: %v", calls)
	}

	// 5. WithoutMiddleware 空参无副作用，非空参触发预期配置 panic
	adminGroup.WithoutMiddleware()
	func() {
		defer func() {
			recovered := recover()
			errVal, ok := recovered.(error)
			if !ok || !errors.Is(errVal, fwroute.ErrInvalidRouteMiddleware) {
				t.Fatalf("非空 WithoutMiddleware 必须抛出 ErrInvalidRouteMiddleware，实际为 %#v", recovered)
			}
		}()
		app2 := NewConsoleAppUninitialized(t.TempDir())
		defer app2.Close()
		app2.Route().Group("/x", func() {
			app2.Route().Get("/y", handler("y"))
		}).WithoutMiddleware(authMw)
	}()
}

func TestRouteFacadeScopedMissPreservesGroupPrefixDomainAndMiddleware(t *testing.T) {
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	var trace []string
	authMw := middleware.Handler(func(req *Request, next func(*Request) *Response) *Response {
		trace = append(trace, "admin_auth")
		return next(req)
	})
	makeHandler := func(name string) func(*Request) *Response {
		return func(*Request) *Response {
			trace = append(trace, name)
			return NewResponse().Content(name)
		}
	}

	routes := app.Route()
	routes.Miss(makeHandler("global_miss"))
	routes.Group("/admin", func() {
		routes.Get("/ok", makeHandler("admin_ok"))
		routes.Miss(makeHandler("admin_miss"))
		routes.Miss(makeHandler("admin_post_miss"), http.MethodPost)
	}).NamedMiddleware("auth", authMw)

	routes.Domain("ops.example.com", func(group *RuleGroup) {
		group.Miss(makeHandler("ops_miss"))
	})

	// 1. /admin 下未命中 GET 请求走 admin_miss 且执行后置 authMw
	trace = trace[:0]
	reqAdminMiss := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/admin/missing", nil))
	matched, _, err := routes.Match(reqAdminMiss)
	if err != nil || matched == nil || !matched.HasMiddleware() {
		t.Fatalf("/admin/missing 未命中分组 Miss 或丢失中间件: matched=%#v err=%v", matched, err)
	}
	_ = matched.ExecuteMiddleware(reqAdminMiss, matched.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(trace, []string{"admin_auth", "admin_miss"}) {
		t.Fatalf("/admin/missing 执行链错误: %v", trace)
	}

	// 2. /admin 下未命中 POST 请求优先走方法专用 admin_post_miss 且执行 authMw
	trace = trace[:0]
	reqAdminPost := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/admin/missing", nil))
	matchedPost, _, err := routes.Match(reqAdminPost)
	if err != nil || matchedPost == nil {
		t.Fatalf("POST /admin/missing 匹配失败: %v", err)
	}
	_ = matchedPost.ExecuteMiddleware(reqAdminPost, matchedPost.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(trace, []string{"admin_auth", "admin_post_miss"}) {
		t.Fatalf("POST /admin/missing 执行链错误: %v", trace)
	}

	// 3. 非 /admin 请求仍命中全局 global_miss，未被组内 Miss 污染覆写
	trace = trace[:0]
	reqPublicMiss := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/public/missing", nil))
	matchedPublic, _, err := routes.Match(reqPublicMiss)
	if err != nil || matchedPublic == nil || matchedPublic.HasMiddleware() {
		t.Fatalf("/public/missing 应命中无鉴权的全局 Miss: matched=%#v err=%v", matchedPublic, err)
	}
	_ = matchedPublic.ExecuteMiddleware(reqPublicMiss, matchedPublic.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(trace, []string{"global_miss"}) {
		t.Fatalf("/public/missing 执行链错误: %v", trace)
	}

	// 4. 域名分组 Miss 仅作用于对应域名
	trace = trace[:0]
	reqOps := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://ops.example.com/unknown", nil))
	matchedOps, _, err := routes.Match(reqOps)
	if err != nil || matchedOps == nil {
		t.Fatalf("域名 Miss 匹配失败: %v", err)
	}
	_ = matchedOps.ExecuteMiddleware(reqOps, matchedOps.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(trace, []string{"ops_miss"}) {
		t.Fatalf("域名 Miss 执行链错误: %v", trace)
	}
}

func TestRouteFacadeNestedOverridesPreservedUnderOuterPostfixModifiers(t *testing.T) {
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	makeHandler := func(name string) func(*Request) *Response {
		return func(*Request) *Response {
			return NewResponse().Content(name)
		}
	}

	routes := app.Route()
	routes.Group("/api", func() {
		routes.Domain("private.example.com", func() {
			routes.Get("/secret/:id", makeHandler("secret"))
		}).Ext("xml").Pattern(map[string]string{"id": `[a-z]+`})

		routes.Group("/nested", func() {
			routes.Get("/sub/:id", makeHandler("sub"))
		}).Domain("sub.example.com").Ext("yaml").Pattern(map[string]string{"id": `[A-Z]+`})

		routes.Domain("chain.example.com").Ext("csv").Pattern(map[string]string{"id": `[0-9]{2}`}).Get("/chain/:id", makeHandler("chain"))

		routes.Get("/public/:id", makeHandler("public"))
	}).Domain("first.example.com").Domain("public.example.com").Ext("json").Pattern(map[string]string{"id": `\d+`})

	// 1. 外层未显式覆盖路由继承外层后置 Domain / Ext / Pattern（含连续链式后置覆盖）
	reqPublic := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://public.example.com/api/public/123.json", nil))
	if matched, params, err := routes.Match(reqPublic); err != nil || matched == nil || params["id"] != "123" {
		t.Fatalf("外层后置修饰路由匹配失败: matched=%#v params=%#v err=%v", matched, params, err)
	}
	reqPublicAlpha := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://public.example.com/api/public/abc.json", nil))
	if matched, _, err := routes.Match(reqPublicAlpha); err != nil || matched != nil {
		t.Fatalf("外层后置 Pattern 应拦截非数字 id: matched=%#v err=%v", matched, err)
	}

	// 2. 嵌套 Domain 分组、嵌套后置分组、嵌套前置链式分组均不被外层后置修饰覆盖
	reqPrivate := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://private.example.com/api/secret/alpha.xml", nil))
	if matched, params, err := routes.Match(reqPrivate); err != nil || matched == nil || params["id"] != "alpha" {
		t.Fatalf("嵌套 Domain/Ext/Pattern 被外层覆盖: matched=%#v params=%#v err=%v", matched, params, err)
	}
	reqPrivateWrongHost := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://public.example.com/api/secret/alpha.xml", nil))
	if matched, _, err := routes.Match(reqPrivateWrongHost); err != nil || matched != nil {
		t.Fatalf("私域路由不应暴露在外层公域域名下: matched=%#v err=%v", matched, err)
	}

	reqSub := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://sub.example.com/api/nested/sub/ABC.yaml", nil))
	if matched, params, err := routes.Match(reqSub); err != nil || matched == nil || params["id"] != "ABC" {
		t.Fatalf("嵌套子组后置修饰被外层覆盖: matched=%#v params=%#v err=%v", matched, params, err)
	}

	reqChain := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://chain.example.com/api/chain/42.csv", nil))
	if matched, params, err := routes.Match(reqChain); err != nil || matched == nil || params["id"] != "42" {
		t.Fatalf("嵌套前置链式修饰被外层覆盖: matched=%#v params=%#v err=%v", matched, params, err)
	}
}

func TestRouteFacadeParameterizedGroupMissExtractsParamsAndMiddleware(t *testing.T) {
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	var trace []string
	tenantMw := middleware.Handler(func(req *Request, next func(*Request) *Response) *Response {
		trace = append(trace, "tenant_mw")
		return next(req)
	})
	makeHandler := func(name string) func(*Request) *Response {
		return func(*Request) *Response {
			trace = append(trace, name)
			return NewResponse().Content(name)
		}
	}

	routes := app.Route()
	routes.Miss(makeHandler("global_miss"))
	routes.Group("/tenants/:tenant", func() {
		routes.Get("/profile", makeHandler("profile"))
		routes.Miss(makeHandler("tenant_miss"))
	}, tenantMw).Pattern(map[string]string{"tenant": `[a-z]+`})

	routes.Group("/tenants/system", func() {
		routes.Miss(makeHandler("system_miss"))
	})

	// 1. 参数化分组下的未命中路径命中 tenant_miss，返回提取的 :tenant 参数并执行分组中间件
	trace = trace[:0]
	reqTenantMiss := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/tenants/acme/missing", nil))
	matched, params, err := routes.Match(reqTenantMiss)
	if err != nil || matched == nil || params["tenant"] != "acme" {
		t.Fatalf("参数化分组 Miss 匹配失败: matched=%#v params=%#v err=%v", matched, params, err)
	}
	if !reflect.DeepEqual(matched.ParameterNames(), []string{"tenant"}) {
		t.Fatalf("参数化 Miss ParameterNames 异常: %v", matched.ParameterNames())
	}
	_ = matched.ExecuteMiddleware(reqTenantMiss, matched.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(trace, []string{"tenant_mw", "tenant_miss"}) {
		t.Fatalf("参数化分组 Miss 中间件执行异常: %v", trace)
	}

	// 2. 不满足分组变量正则的前缀不命中 tenant_miss，回退到 global_miss
	trace = trace[:0]
	reqInvalidTenant := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/tenants/12345/missing", nil))
	matchedFallback, fallbackParams, err := routes.Match(reqInvalidTenant)
	if err != nil || matchedFallback == nil || len(fallbackParams) != 0 {
		t.Fatalf("不满足约束的参数化前缀应回退到全局 Miss: matched=%#v params=%#v err=%v", matchedFallback, fallbackParams, err)
	}
	_ = matchedFallback.ExecuteMiddleware(reqInvalidTenant, matchedFallback.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(trace, []string{"global_miss"}) {
		t.Fatalf("回退全局 Miss 执行异常: %v", trace)
	}

	// 3. 同深度下字面量分组 /tenants/system 优先于参数化分组 /tenants/:tenant
	trace = trace[:0]
	reqSystem := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/tenants/system/missing", nil))
	matchedSystem, sysParams, err := routes.Match(reqSystem)
	if err != nil || matchedSystem == nil || len(sysParams) != 0 {
		t.Fatalf("字面量分组 Miss 匹配失败: matched=%#v params=%#v err=%v", matchedSystem, sysParams, err)
	}
	_ = matchedSystem.ExecuteMiddleware(reqSystem, matchedSystem.Handler().(func(*Request) *Response))
	if !reflect.DeepEqual(trace, []string{"system_miss"}) {
		t.Fatalf("字面量分组 Miss 优先级异常: %v", trace)
	}
}
