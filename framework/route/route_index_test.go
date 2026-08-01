package route

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	fwcontext "thinkgo/framework/context"
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
	requestParts, normalizedPath, err := requestPathParts(req)
	if err != nil {
		return nil, nil, err
	}
	method, err := normalizeRouteMethod(req.Method())
	if err != nil || method == anyMethod {
		return nil, nil, fmt.Errorf("%w: 请求方法 %q 非法", ErrInvalidRoute, req.Method())
	}
	host := normalizeRequestHost(req.Host())
	methods := []string{method}
	if method == http.MethodHead {
		methods = append(methods, http.MethodGet)
	}
	methods = append(methods, anyMethod)
	for _, candidateMethod := range methods {
		if matched := linearStaticMatch(router.staticRoutes[candidateMethod], requestParts, host); matched != nil {
			return matched, nil, nil
		}
		if matched, params := linearDynamicMatch(router.dynamicRoutes[candidateMethod], requestParts, host); matched != nil {
			return matched, params, nil
		}
	}
	allowed := linearAllowedMethods(router, requestParts, host)
	if len(allowed) > 0 {
		if method == http.MethodOptions {
			return automaticOptionsRoute(allowed), nil, nil
		}
		return nil, nil, &MethodNotAllowedError{Allowed: allowed}
	}
	if router.autoRoute && isSafeAutoRouteParts(requestParts) {
		autoAllowed := []string{http.MethodGet, http.MethodHead, http.MethodOptions}
		if method == http.MethodOptions {
			return automaticOptionsRoute(autoAllowed), nil, nil
		}
		if method != http.MethodGet && method != http.MethodHead {
			return nil, nil, &MethodNotAllowedError{Allowed: autoAllowed}
		}
		if automatic := router.resolveAutoRoute(normalizedPath, requestParts); automatic != nil {
			return automatic, nil, nil
		}
	}
	if router.missRoute != nil {
		return router.missRoute, nil, nil
	}
	return nil, nil, nil
}

func linearStaticMatch(routes map[string][]*Route, requestParts []string, host string) *Route {
	candidates := routes[pathPartsKey(requestParts)]
	for _, candidate := range candidates {
		if candidate.domain != "" && candidate.domain == host {
			return candidate
		}
	}
	for _, candidate := range candidates {
		if candidate.domain == "" {
			return candidate
		}
	}
	return nil
}

func linearDynamicMatch(routes []*Route, requestParts []string, host string) (*Route, map[string]string) {
	for _, exactDomain := range []bool{true, false} {
		for _, candidate := range routes {
			if exactDomain {
				if candidate.domain == "" || candidate.domain != host {
					continue
				}
			} else if candidate.domain != "" {
				continue
			}
			if matched, params := candidate.matchRequestParts(requestParts); matched {
				return candidate, params
			}
		}
	}
	return nil, nil
}

func linearAllowedMethods(router *Router, requestParts []string, host string) []string {
	methods := make(map[string]bool)
	staticKey := pathPartsKey(requestParts)
	for method, indexedPaths := range router.staticRoutes {
		if !hasMatchingRouteDomain(indexedPaths[staticKey], host) {
			continue
		}
		if method == anyMethod {
			return nil
		}
		methods[method] = true
	}
	for method, candidates := range router.dynamicRoutes {
		matchedMethod := false
		for _, candidate := range candidates {
			if candidate.domain != "" && candidate.domain != host {
				continue
			}
			if matched, _ := candidate.matchRequestParts(requestParts); matched {
				matchedMethod = true
				break
			}
		}
		if !matchedMethod {
			continue
		}
		if method == anyMethod {
			return nil
		}
		methods[method] = true
	}
	if len(methods) == 0 {
		return nil
	}
	if methods[http.MethodGet] {
		methods[http.MethodHead] = true
	}
	methods[http.MethodOptions] = true
	return sortHTTPMethods(methods)
}

func assertRouteMatchParity(t *testing.T, actual *Route, actualParams map[string]string, actualErr error, expected *Route, expectedParams map[string]string, expectedErr error) {
	t.Helper()
	if (actualErr == nil) != (expectedErr == nil) {
		t.Fatalf("错误存在性不一致: actual=%v expected=%v", actualErr, expectedErr)
	}
	if actualErr != nil {
		var actualMethodErr, expectedMethodErr *MethodNotAllowedError
		actualIsMethod := errors.As(actualErr, &actualMethodErr)
		expectedIsMethod := errors.As(expectedErr, &expectedMethodErr)
		if actualIsMethod || expectedIsMethod {
			if !actualIsMethod || !expectedIsMethod || !reflect.DeepEqual(actualMethodErr.Allowed, expectedMethodErr.Allowed) {
				t.Fatalf("Allow 方法集合不一致: actual=%#v expected=%#v", actualMethodErr, expectedMethodErr)
			}
			return
		}
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
