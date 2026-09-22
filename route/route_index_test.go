package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// newRouteIndexParityRouter 构造覆盖方法、域名、可选参数、正则和扩展名的固定路由集合。
func newRouteIndexParityRouter(t *testing.T) *Router {
	t.Helper()
	router := NewRouter()
	registrations := []struct {
		method  string
		path    string
		handler string
		domain  string
	}{
		{method: http.MethodGet, path: "/users/:id", handler: "User@Show"},
		{method: http.MethodGet, path: "/users/:id/profile", handler: "User@Profile"},
		{method: http.MethodPost, path: "/users/:id", handler: "User@Update"},
		{method: http.MethodGet, path: "/docs/:lang?/:page?", handler: "Docs@Show"},
		{method: http.MethodGet, path: "/digits/:id", handler: "Digits@Show"},
		{method: http.MethodGet, path: "/files/:name", handler: "File@Show"},
		{method: anyMethod, path: "/wild/:name", handler: "Wild@Show"},
		{method: http.MethodGet, path: "/users/:id", handler: "API@Show", domain: "api.example.com"},
	}
	var registered []*Route
	for _, definition := range registrations {
		var route *Route
		var err error
		register := func(target *Group) error {
			switch definition.method {
			case http.MethodGet:
				route, err = target.Get(definition.path, definition.handler)
			case http.MethodPost:
				route, err = target.Post(definition.path, definition.handler)
			default:
				route, err = target.Any(definition.path, definition.handler)
			}
			return err
		}
		if definition.domain == "" {
			err = register(router.rootGroup())
		} else {
			err = router.Domain(definition.domain, register)
		}
		if err != nil || route == nil {
			t.Fatalf("注册路由 %s %s 失败: %v", definition.method, definition.path, err)
		}
		registered = append(registered, route)
	}
	if err := registered[5].WithExtension("json"); err != nil {
		t.Fatalf("设置扩展名失败: %v", err)
	}
	if err := registered[4].WithPattern("id", `[0-9]+`); err != nil {
		t.Fatalf("设置正则约束失败: %v", err)
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结路由失败: %v", err)
	}
	return router
}

// TestRouteIndexParity 验证动态索引与冻结前的线性语义参考实现完全一致。
func TestRouteIndexParity(t *testing.T) {
	router := newRouteIndexParityRouter(t)
	if router.dynamicIndex == nil {
		t.Fatal("冻结后必须构建动态路由索引")
	}
	tests := []struct {
		name   string
		method string
		host   string
		path   string
	}{
		{name: "required parameter", method: http.MethodGet, host: "example.com", path: "/users/42"},
		{name: "specific literal suffix", method: http.MethodGet, host: "example.com", path: "/users/42/profile"},
		{name: "exact domain wins", method: http.MethodGet, host: "api.example.com", path: "/users/42"},
		{name: "optional omitted", method: http.MethodGet, host: "example.com", path: "/docs"},
		{name: "optional included", method: http.MethodGet, host: "example.com", path: "/docs/zh-cn/home"},
		{name: "regex accepted", method: http.MethodGet, host: "example.com", path: "/digits/123"},
		{name: "regex rejected", method: http.MethodGet, host: "example.com", path: "/digits/abc"},
		{name: "extension accepted", method: http.MethodGet, host: "example.com", path: "/files/report.json"},
		{name: "extension rejected", method: http.MethodGet, host: "example.com", path: "/files/report"},
		{name: "any method", method: http.MethodPatch, host: "example.com", path: "/wild/value"},
		{name: "trailing slash preserved", method: http.MethodPatch, host: "example.com", path: "/wild/value/"},
		{name: "method not allowed", method: http.MethodDelete, host: "example.com", path: "/users/42"},
		{name: "automatic options", method: http.MethodOptions, host: "example.com", path: "/users/42"},
		{name: "missing", method: http.MethodGet, host: "example.com", path: "/missing"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			raw := httptest.NewRequest(testCase.method, "http://"+testCase.host+testCase.path, nil)
			req := fwcontext.MustNewRequest(raw)
			actual, actualParams, actualErr := router.Match(req)
			expected, expectedParams, expectedErr := linearRouteMatch(router, req)
			assertRouteMatchParity(t, actual, actualParams, actualErr, expected, expectedParams, expectedErr)
		})
	}
}

// TestRouteIndexRandomParity 使用固定种子覆盖不同方法、域名和路径形态。
func TestRouteIndexRandomParity(t *testing.T) {
	router := newRouteIndexParityRouter(t)
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodOptions, http.MethodPatch}
	hosts := []string{"example.com", "api.example.com", "other.example.com"}
	paths := []string{"/users/1", "/users/2/profile", "/docs", "/docs/en", "/docs/en/home", "/digits/7", "/digits/x", "/files/a.json", "/files/a", "/wild/x", "/unknown"}
	for index := 0; index < 512; index++ {
		method := methods[(index*17+3)%len(methods)]
		host := hosts[(index*11+1)%len(hosts)]
		path := paths[(index*29+5)%len(paths)]
		raw := httptest.NewRequest(method, "http://"+host+path, nil)
		req := fwcontext.MustNewRequest(raw)
		actual, actualParams, actualErr := router.Match(req)
		expected, expectedParams, expectedErr := linearRouteMatch(router, req)
		assertRouteMatchParity(t, actual, actualParams, actualErr, expected, expectedParams, expectedErr)
	}
}

// TestRouteIndexExtensionPathShapeParity 覆盖后缀附着在字面量、参数以及
// 省略可选参数后的前一段时的索引语义，并验证域名和正则优先级不被索引改变。
func TestRouteIndexExtensionPathShapeParity(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("关闭自动路由失败: %v", err)
	}
	if err := router.SetCompleteMatch(true); err != nil {
		t.Fatalf("启用完整匹配失败: %v", err)
	}
	if _, err := router.Get("/articles/:id/detail", "Article@Detail"); err != nil {
		t.Fatalf("注册字面量结尾路由失败: %v", err)
	}
	if _, err := router.Get("/docs/:lang?/:page?", "Docs@Show"); err != nil {
		t.Fatalf("注册可选参数路由失败: %v", err)
	}
	exportRoute, err := router.Get("/exports/:id/archive", "Export@Archive")
	if err != nil {
		t.Fatalf("注册强制后缀路由失败: %v", err)
	}
	if err = exportRoute.WithExtension("json"); err != nil {
		t.Fatalf("设置强制后缀失败: %v", err)
	}
	digitRoute, err := router.Get("/digits/:id/result", "Digit@Result")
	if err != nil {
		t.Fatalf("注册正则路由失败: %v", err)
	}
	if err = digitRoute.WithPattern("id", `[0-9]+`); err != nil {
		t.Fatalf("设置正则约束失败: %v", err)
	}
	if _, err = router.Get("/tenant/:id/detail", "Tenant@Public"); err != nil {
		t.Fatalf("注册公共域名路由失败: %v", err)
	}
	if err = router.Domain("api.example.com", func(group *Group) error {
		_, registerErr := group.Get("/tenant/:id/detail", "Tenant@API")
		return registerErr
	}); err != nil {
		t.Fatalf("注册精确域名路由失败: %v", err)
	}
	if err = router.Freeze(); err != nil {
		t.Fatalf("冻结路由失败: %v", err)
	}

	tests := []struct {
		name        string
		host        string
		path        string
		wantHandler string
		wantParams  map[string]string
	}{
		{name: "literal without extension", host: "example.com", path: "/articles/42/detail", wantHandler: "Article@Detail", wantParams: map[string]string{"id": "42"}},
		{name: "literal with optional extension", host: "example.com", path: "/articles/42/detail.html", wantHandler: "Article@Detail", wantParams: map[string]string{"id": "42"}},
		{name: "optional parameters omitted before extension", host: "example.com", path: "/docs.html", wantHandler: "Docs@Show"},
		{name: "one optional parameter before extension", host: "example.com", path: "/docs/en.html", wantHandler: "Docs@Show", wantParams: map[string]string{"lang": "en"}},
		{name: "all optional parameters before extension", host: "example.com", path: "/docs/en/start.html", wantHandler: "Docs@Show", wantParams: map[string]string{"lang": "en", "page": "start"}},
		{name: "required extension", host: "example.com", path: "/exports/7/archive.json", wantHandler: "Export@Archive", wantParams: map[string]string{"id": "7"}},
		{name: "required extension missing", host: "example.com", path: "/exports/7/archive"},
		{name: "regex accepted", host: "example.com", path: "/digits/12/result.html", wantHandler: "Digit@Result", wantParams: map[string]string{"id": "12"}},
		{name: "regex rejected", host: "example.com", path: "/digits/x/result.html"},
		{name: "exact domain with extension", host: "api.example.com", path: "/tenant/9/detail.html", wantHandler: "Tenant@API", wantParams: map[string]string{"id": "9"}},
		{name: "public domain with extension", host: "other.example.com", path: "/tenant/9/detail.html", wantHandler: "Tenant@Public", wantParams: map[string]string{"id": "9"}},
		{name: "unregistered extension rejected", host: "example.com", path: "/articles/42/detail.xml"},
		{name: "extension remains case sensitive", host: "example.com", path: "/articles/42/detail.HTML"},
		{name: "trailing slash preserved", host: "example.com", path: "/articles/42/detail.html/"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://"+testCase.host+testCase.path, nil))
			actual, actualParams, actualErr := router.Match(request)
			expected, expectedParams, expectedErr := linearRouteMatch(router, request)
			assertRouteMatchParity(t, actual, actualParams, actualErr, expected, expectedParams, expectedErr)
			if testCase.wantHandler == "" {
				if actual != nil {
					t.Fatalf("路径 %q 不应命中，实际处理器=%v", testCase.path, actual.Handler())
				}
				return
			}
			if actual == nil || actual.Handler() != testCase.wantHandler {
				t.Fatalf("路径 %q 应命中 %s，实际路由=%#v", testCase.path, testCase.wantHandler, actual)
			}
			if !reflect.DeepEqual(actualParams, testCase.wantParams) {
				t.Fatalf("路径 %q 参数不正确，实际=%#v 期望=%#v", testCase.path, actualParams, testCase.wantParams)
			}
		})
	}
}

// TestRouteIndexPrefixCaseInsensitiveExtensionParity 组合覆盖动态路由、前缀匹配、
// 字面量大小写不敏感与可选/强制后缀，确保索引只裁剪候选集而不改变线性语义。
func TestRouteIndexPrefixCaseInsensitiveExtensionParity(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("关闭自动路由失败: %v", err)
	}
	if err := router.SetCompleteMatch(false); err != nil {
		t.Fatalf("启用前缀匹配失败: %v", err)
	}
	if err := router.SetCaseSensitive(false); err != nil {
		t.Fatalf("启用大小写不敏感匹配失败: %v", err)
	}
	if _, err := router.Get("/Reports/:category/Detail", "Report@Detail"); err != nil {
		t.Fatalf("注册默认可选后缀动态路由失败: %v", err)
	}
	exportRoute, err := router.Get("/Exports/:id", "Export@Show")
	if err != nil {
		t.Fatalf("注册强制后缀动态路由失败: %v", err)
	}
	if err = exportRoute.WithExtension("json"); err != nil {
		t.Fatalf("设置动态路由强制后缀失败: %v", err)
	}
	if err = router.Freeze(); err != nil {
		t.Fatalf("冻结组合差分路由失败: %v", err)
	}

	tests := []struct {
		name        string
		path        string
		wantHandler string
		wantParams  map[string]string
	}{
		{name: "optional extension omitted", path: "/REPORTS/News/DETAIL", wantHandler: "Report@Detail", wantParams: map[string]string{"category": "News"}},
		{name: "optional extension present", path: "/REPORTS/News/DETAIL.html", wantHandler: "Report@Detail", wantParams: map[string]string{"category": "News"}},
		{name: "prefix suffix without extension", path: "/REPORTS/News/DETAIL/Archive", wantHandler: "Report@Detail", wantParams: map[string]string{"category": "News"}},
		{name: "prefix suffix carries extension", path: "/REPORTS/News/DETAIL/Archive.html", wantHandler: "Report@Detail", wantParams: map[string]string{"category": "News"}},
		{name: "required extension present", path: "/EXPORTS/42.json", wantHandler: "Export@Show", wantParams: map[string]string{"id": "42"}},
		{name: "required extension on ignored suffix", path: "/EXPORTS/42/Archive.json", wantHandler: "Export@Show", wantParams: map[string]string{"id": "42"}},
		{name: "required extension absent", path: "/EXPORTS/42/Archive"},
		{name: "extension remains case sensitive", path: "/EXPORTS/42.JSON"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+testCase.path, nil))
			actual, actualParams, actualErr := router.Match(request)
			expected, expectedParams, expectedErr := linearRouteMatch(router, request)
			assertRouteMatchParity(t, actual, actualParams, actualErr, expected, expectedParams, expectedErr)
			if testCase.wantHandler == "" {
				if actual != nil {
					t.Fatalf("路径 %q 不应命中，实际处理器=%v", testCase.path, actual.Handler())
				}
				return
			}
			if actual == nil || actual.Handler() != testCase.wantHandler {
				t.Fatalf("路径 %q 应命中 %s，实际路由=%#v", testCase.path, testCase.wantHandler, actual)
			}
			if !reflect.DeepEqual(actualParams, testCase.wantParams) {
				t.Fatalf("路径 %q 参数不正确，实际=%#v 期望=%#v", testCase.path, actualParams, testCase.wantParams)
			}
		})
	}
}

// FuzzRouteIndexParity 用随机输入验证索引只缩小候选集，不改变最终语义判定。
func FuzzRouteIndexParity(f *testing.F) {
	for _, seed := range []struct {
		method string
		path   string
		host   string
	}{
		{method: http.MethodGet, path: "/users/1", host: "example.com"},
		{method: http.MethodPost, path: "/docs/en", host: "api.example.com"},
		{method: http.MethodOptions, path: "/unknown", host: "other.example.com"},
	} {
		f.Add(seed.method, seed.path, seed.host)
	}
	router := newRouteIndexParityRouterForFuzz()
	f.Fuzz(func(t *testing.T, method, path, host string) {
		if len(path) > maxRoutePathLength+32 {
			t.Skip()
		}
		if path == "" {
			path = "/"
		}
		if path[0] != '/' {
			path = "/" + path
		}
		if host != "api.example.com" {
			host = "example.com"
		}
		raw := &http.Request{Method: method, URL: &url.URL{Path: path}, Host: host}
		req := fwcontext.MustNewRequest(raw)
		actual, actualParams, actualErr := router.Match(req)
		expected, expectedParams, expectedErr := linearRouteMatch(router, req)
		assertRouteMatchParity(t, actual, actualParams, actualErr, expected, expectedParams, expectedErr)
	})
}

func newRouteIndexParityRouterForFuzz() *Router {
	router := NewRouter()
	registered := make([]*Route, 0, 8)
	for _, definition := range []struct {
		method  string
		path    string
		handler string
	}{
		{method: http.MethodGet, path: "/users/:id", handler: "User@Show"},
		{method: http.MethodGet, path: "/users/:id/profile", handler: "User@Profile"},
		{method: http.MethodPost, path: "/docs/:lang?/:page?", handler: "Docs@Show"},
		{method: http.MethodGet, path: "/digits/:id", handler: "Digits@Show"},
		{method: http.MethodGet, path: "/files/:name", handler: "File@Show"},
		{method: anyMethod, path: "/wild/:name", handler: "Wild@Show"},
	} {
		var route *Route
		var err error
		if definition.method == anyMethod {
			route, err = router.Any(definition.path, definition.handler)
		} else {
			route, err = router.Add(definition.method, definition.path, definition.handler)
		}
		if err != nil {
			return router
		}
		registered = append(registered, route)
	}
	_ = registered[3].WithPattern("id", `[0-9]+`)
	_ = registered[4].WithExtension("json")
	_ = router.Freeze()
	return router
}

func linearRouteMatch(router *Router, req *fwcontext.Request) (*Route, map[string]string, error) {
	requestParts, normalizedPath, trailingSlash, err := requestPathPartsWithOptions(req, router.removeSlash)
	if err != nil {
		return nil, nil, err
	}
	method, err := normalizeRouteMethod(req.Method())
	if err != nil || method == anyMethod {
		return nil, nil, fmt.Errorf("%w: 请求方法 %q 非法", ErrInvalidRoute, req.Method())
	}
	host := normalizeRequestHost(req.Host())
	methods := []string{method}
	methods = append(methods, anyMethod)
	for _, candidateMethod := range methods {
		if matched := linearStaticMatch(router.staticRoutes[candidateMethod], requestParts, trailingSlash, host); matched != nil {
			return matched, nil, nil
		}
		if matched, params := linearDynamicMatch(router.dynamicRoutes[candidateMethod], requestParts, trailingSlash, host); matched != nil {
			return matched, params, nil
		}
	}
	if miss := router.matchMissRoute(method); miss != nil {
		return miss, nil, nil
	}
	if method == http.MethodOptions {
		if router.autoRoute {
			return automaticOptionsRoute(), nil, nil
		}
		return nil, nil, nil
	}
	if router.autoRoute && isSafeAutoRouteParts(requestParts) {
		// ThinkPHP 自动调度不先按 HTTP 方法裁剪控制器动作。
		if automatic := router.resolveAutoRoute(normalizedPath, requestParts); automatic != nil {
			return automatic, nil, nil
		}
	}
	return nil, nil, nil
}

func linearStaticMatch(routes map[string][]*Route, requestParts []string, trailingSlash bool, host string) *Route {
	candidates := routes[pathPartsKey(requestParts)]
	for _, candidate := range candidates {
		matched, _ := candidate.matchRequestPartsWithOptions(requestParts, trailingSlash)
		if matched && candidate.domain != "" && candidate.domain == host {
			return candidate
		}
	}
	for _, candidate := range candidates {
		matched, _ := candidate.matchRequestPartsWithOptions(requestParts, trailingSlash)
		if matched && candidate.domain == "" {
			return candidate
		}
	}
	return nil
}

func linearDynamicMatch(routes []*Route, requestParts []string, trailingSlash bool, host string) (*Route, map[string]string) {
	for _, exactDomain := range []bool{true, false} {
		for _, candidate := range routes {
			if exactDomain {
				if candidate.domain == "" || candidate.domain != host {
					continue
				}
			} else if candidate.domain != "" {
				continue
			}
			if matched, params := candidate.matchRequestPartsWithOptions(requestParts, trailingSlash); matched {
				return candidate, params
			}
		}
	}
	return nil, nil
}

func assertRouteMatchParity(t *testing.T, actual *Route, actualParams map[string]string, actualErr error, expected *Route, expectedParams map[string]string, expectedErr error) {
	t.Helper()
	if (actualErr == nil) != (expectedErr == nil) {
		t.Fatalf("错误存在性不一致: actual=%v expected=%v", actualErr, expectedErr)
	}
	if actualErr != nil {
		if expectedErr == nil || actualErr.Error() != expectedErr.Error() {
			t.Fatalf("错误语义不一致: actual=%v expected=%v", actualErr, expectedErr)
		}
		return
	}
	if actual == nil || expected == nil {
		if actual != nil || expected != nil {
			t.Fatalf("路由是否命中不一致: actual=%#v expected=%#v", actual, expected)
		}
		return
	}
	if actual != expected && (actual.Path() != expected.Path() || actual.Method() != expected.Method() || actual.domain != expected.domain) {
		t.Fatalf("路由身份不一致: actual=%s %s %s expected=%s %s %s", actual.Method(), actual.Path(), actual.domain, expected.Method(), expected.Path(), expected.domain)
	}
	if !reflect.DeepEqual(actualParams, expectedParams) {
		t.Fatalf("路由参数不一致: actual=%#v expected=%#v", actualParams, expectedParams)
	}
}
