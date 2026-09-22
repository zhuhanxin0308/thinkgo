package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestStaticPrefixMatchKeepsFrozenIndexBackingImmutable 验证匹配器不会写入冻结索引切片的备用容量。
func TestStaticPrefixMatchKeepsFrozenIndexBackingImmutable(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		verify func(*testing.T, *Route, error)
	}{
		{
			name:   "命中扫描",
			method: http.MethodGet,
			verify: func(t *testing.T, matched *Route, err error) {
				t.Helper()
				if err != nil || matched == nil || matched.Handler() != "Catalog@Items" {
					t.Fatalf("精确静态路由匹配失败，route=%#v err=%v", matched, err)
				}
			},
		},
		{
			name:   "URL 调度扫描",
			method: http.MethodDelete,
			verify: func(t *testing.T, matched *Route, err error) {
				t.Helper()
				if err != nil || matched == nil || !matched.IsAuto() || matched.Handler() != "catalog/items" {
					t.Fatalf("未注册方法应继续 ThinkPHP URL 调度，route=%#v err=%v", matched, err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			router := newStaticPrefixConcurrencyRouter(t, 1)
			allSlots, guardIndex, guard := installStaticIndexBackingGuard(t, router)
			request := fwcontext.MustNewRequest(httptest.NewRequest(testCase.method, "http://example.com/catalog/items", nil))
			matched, _, err := router.Match(request)
			testCase.verify(t, matched, err)
			if allSlots[guardIndex] != guard {
				t.Fatal("非完整匹配不得通过 append 覆盖冻结静态索引的备用容量")
			}
		})
	}
}

// TestRouterConcurrentStaticPrefixMatchKeepsIndexReadOnly 验证并发匹配只读共享同一个冻结索引。
func TestRouterConcurrentStaticPrefixMatchKeepsIndexReadOnly(t *testing.T) {
	const (
		prefixRouteCount = 64
		workerCount      = 32
		matchCount       = 100
	)
	router := newStaticPrefixConcurrencyRouter(t, prefixRouteCount)
	key := router.pathPartsKey([]string{"catalog", "items"})
	indexed := router.staticRoutes[http.MethodGet][key]
	backing := make([]*Route, len(indexed), len(indexed)+len(router.routes)+1)
	copy(backing, indexed)
	router.staticRoutes[http.MethodGet][key] = backing

	start := make(chan struct{})
	errorsChannel := make(chan error, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/catalog/items", nil))
			<-start
			for attempt := 0; attempt < matchCount; attempt++ {
				matched, _, err := router.Match(request)
				if err != nil || matched == nil || matched.Handler() != "Catalog@Items" {
					errorsChannel <- fmt.Errorf("并发静态路由匹配失败，route=%#v err=%v", matched, err)
					return
				}
			}
		}()
	}
	close(start)
	workers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
}

func newStaticPrefixConcurrencyRouter(t *testing.T, prefixRouteCount int) *Router {
	t.Helper()
	router := NewRouter()
	if err := router.SetCompleteMatch(false); err != nil {
		t.Fatalf("设置非完整匹配失败: %v", err)
	}
	if _, err := router.Get("/catalog/items", "Catalog@Items"); err != nil {
		t.Fatalf("注册精确静态路由失败: %v", err)
	}
	for index := 0; index < prefixRouteCount; index++ {
		path := fmt.Sprintf("/prefix-%d", index)
		if _, err := router.Get(path, "Prefix@Handle"); err != nil {
			t.Fatalf("注册第 %d 条前缀路由失败: %v", index, err)
		}
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结路由失败: %v", err)
	}
	return router
}

func installStaticIndexBackingGuard(t *testing.T, router *Router) ([]*Route, int, *Route) {
	t.Helper()
	key := router.pathPartsKey([]string{"catalog", "items"})
	indexed := router.staticRoutes[http.MethodGet][key]
	if len(indexed) != 1 {
		t.Fatalf("精确静态索引应只有一条路由，实际为 %d", len(indexed))
	}
	backing := make([]*Route, len(indexed), len(indexed)+len(router.routes)+1)
	copy(backing, indexed)
	allSlots := backing[:cap(backing)]
	guardIndex := len(indexed)
	guard := &Route{path: "__frozen_index_guard__"}
	allSlots[guardIndex] = guard
	router.staticRoutes[http.MethodGet][key] = backing
	return allSlots, guardIndex, guard
}

// TestStaticPrefixCandidateSegmentsPreserveRoutingSemantics 覆盖分段候选的优先级、方法、域名、后缀和斜杠语义。
func TestStaticPrefixCandidateSegmentsPreserveRoutingSemantics(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("关闭自动路由失败: %v", err)
	}
	if err := router.SetCaseSensitive(false); err != nil {
		t.Fatalf("设置大小写不敏感失败: %v", err)
	}
	if err := router.SetCompleteMatch(false); err != nil {
		t.Fatalf("设置非完整匹配失败: %v", err)
	}
	if err := router.SetRemoveSlash(false); err != nil {
		t.Fatalf("设置斜杠规则失败: %v", err)
	}
	if _, err := router.Get("/API/Users", "Public@Exact"); err != nil {
		t.Fatalf("注册公共精确路由失败: %v", err)
	}
	if _, err := router.Get("/API", "Public@Prefix"); err != nil {
		t.Fatalf("注册公共前缀路由失败: %v", err)
	}
	if err := router.Domain("api.example.com", func(group *Group) error {
		_, registerErr := group.Get("/API", "Domain@Prefix")
		return registerErr
	}); err != nil {
		t.Fatalf("注册域名前缀路由失败: %v", err)
	}
	if _, err := router.Head("/API", "Head@Prefix"); err != nil {
		t.Fatalf("注册 HEAD 前缀路由失败: %v", err)
	}
	if _, err := router.Post("/API", "Post@Prefix"); err != nil {
		t.Fatalf("注册 POST 前缀路由失败: %v", err)
	}
	if _, err := router.Any("/wild", "Wild@Prefix"); err != nil {
		t.Fatalf("注册 ANY 前缀路由失败: %v", err)
	}
	if _, err := router.Options("/manual", "Options@Prefix"); err != nil {
		t.Fatalf("注册显式 OPTIONS 前缀路由失败: %v", err)
	}
	extensionRoute, err := router.Get("/files", "File@Prefix")
	if err != nil {
		t.Fatalf("注册后缀前缀路由失败: %v", err)
	}
	if err = extensionRoute.WithExtension("json"); err != nil {
		t.Fatalf("设置强制后缀失败: %v", err)
	}
	if _, err = router.Get("/trailing", "Trailing@Prefix"); err != nil {
		t.Fatalf("注册斜杠前缀路由失败: %v", err)
	}

	matchCases := []struct {
		name     string
		method   string
		host     string
		path     string
		handler  string
		wantMiss bool
	}{
		{name: "域名前缀优先于公共精确", method: http.MethodGet, host: "api.example.com", path: "/api/users", handler: "Domain@Prefix"},
		{name: "公共精确优先于公共前缀", method: http.MethodGet, host: "www.example.com", path: "/api/users", handler: "Public@Exact"},
		{name: "显式 HEAD 优先于 GET 回落", method: http.MethodHead, host: "api.example.com", path: "/api/users", handler: "Head@Prefix"},
		{name: "ANY 支持任意方法", method: http.MethodPatch, host: "example.com", path: "/wild/value", handler: "Wild@Prefix"},
		{name: "显式 OPTIONS 优先于自动响应", method: http.MethodOptions, host: "example.com", path: "/manual/value", handler: "Options@Prefix"},
		{name: "强制后缀接受匹配", method: http.MethodGet, host: "example.com", path: "/files/value.json", handler: "File@Prefix"},
		{name: "强制后缀拒绝缺失", method: http.MethodGet, host: "example.com", path: "/files/value", wantMiss: true},
		{name: "未声明末尾斜杠接受同形请求", method: http.MethodGet, host: "example.com", path: "/trailing/value", handler: "Trailing@Prefix"},
		{name: "未声明末尾斜杠拒绝带斜杠请求", method: http.MethodGet, host: "example.com", path: "/trailing/value/", wantMiss: true},
	}
	for _, testCase := range matchCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := fwcontext.MustNewRequest(httptest.NewRequest(testCase.method, "http://"+testCase.host+testCase.path, nil))
			matched, _, matchErr := router.Match(request)
			if matchErr != nil {
				t.Fatalf("路由匹配返回意外错误: %v", matchErr)
			}
			if testCase.wantMiss {
				if matched != nil {
					t.Fatalf("请求应未命中，实际为 %#v", matched)
				}
				return
			}
			if matched == nil || matched.Handler() != testCase.handler {
				t.Fatalf("路由优先级错误，route=%#v want=%s", matched, testCase.handler)
			}
		})
	}

	deleteRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodDelete, "http://api.example.com/api/users", nil))
	matched, _, matchErr := router.Match(deleteRequest)
	if matched != nil || matchErr != nil {
		t.Fatalf("强制路由下未注册方法应保持未命中，route=%#v err=%v", matched, matchErr)
	}

	optionsRequest := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodOptions, "http://api.example.com/api/users", nil))
	optionsRoute, _, matchErr := router.Match(optionsRequest)
	if matchErr != nil || optionsRoute != nil {
		t.Fatalf("强制路由下不得生成自动 OPTIONS，route=%#v err=%v", optionsRoute, matchErr)
	}
}

// TestStaticPrefixCandidateSegmentsMatchOwnedMergeReference 用显式私有副本复现旧顺序并做组合差分。
func TestStaticPrefixCandidateSegmentsMatchOwnedMergeReference(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatalf("关闭自动路由失败: %v", err)
	}
	if err := router.SetCaseSensitive(false); err != nil {
		t.Fatalf("设置大小写不敏感失败: %v", err)
	}
	if err := router.SetCompleteMatch(false); err != nil {
		t.Fatalf("设置非完整匹配失败: %v", err)
	}
	if err := router.SetRemoveSlash(false); err != nil {
		t.Fatalf("设置斜杠规则失败: %v", err)
	}
	registrations := []struct {
		method  string
		path    string
		handler string
	}{
		{method: http.MethodGet, path: "/API/Users", handler: "Public@Exact"},
		{method: http.MethodGet, path: "/API", handler: "Public@Prefix"},
		{method: http.MethodHead, path: "/API", handler: "Head@Prefix"},
		{method: http.MethodPost, path: "/API", handler: "Post@Prefix"},
		{method: anyMethod, path: "/wild", handler: "Wild@Prefix"},
		{method: http.MethodOptions, path: "/manual", handler: "Options@Prefix"},
	}
	for _, registration := range registrations {
		if _, err := router.Add(registration.method, registration.path, registration.handler); err != nil {
			t.Fatalf("注册差分路由 %s %s 失败: %v", registration.method, registration.path, err)
		}
	}
	if err := router.Domain("api.example.com", func(group *Group) error {
		_, registerErr := group.Get("/API", "Domain@Prefix")
		return registerErr
	}); err != nil {
		t.Fatalf("注册差分域名路由失败: %v", err)
	}
	extensionRoute, err := router.Get("/files", "File@Prefix")
	if err != nil {
		t.Fatalf("注册差分后缀路由失败: %v", err)
	}
	if err = extensionRoute.WithExtension("json"); err != nil {
		t.Fatalf("设置差分后缀失败: %v", err)
	}
	if err = router.Freeze(); err != nil {
		t.Fatalf("冻结差分路由失败: %v", err)
	}

	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete, http.MethodOptions, http.MethodPatch}
	hosts := []string{"api.example.com", "www.example.com", "other.example.com"}
	paths := []string{"/api/users", "/api/users/extra", "/api/users/", "/wild/value", "/manual/value", "/files/value.json", "/files/value", "/missing"}
	for _, method := range methods {
		for _, host := range hosts {
			for _, path := range paths {
				name := method + " " + host + path
				t.Run(name, func(t *testing.T) {
					request := fwcontext.MustNewRequest(httptest.NewRequest(method, "http://"+host+path, nil))
					actual, actualParams, actualErr := router.Match(request)
					expected, expectedParams, expectedErr := ownedMergeStaticRouteMatch(router, request)
					assertRouteMatchParity(t, actual, actualParams, actualErr, expected, expectedParams, expectedErr)
				})
			}
		}
	}
}

// ownedMergeStaticRouteMatch 使用新建切片复现修复前的候选拼接顺序，只作为差分参考实现。
func ownedMergeStaticRouteMatch(router *Router, request *fwcontext.Request) (*Route, map[string]string, error) {
	requestParts, normalizedPath, trailingSlash, err := requestPathPartsWithOptions(request, router.removeSlash)
	if err != nil {
		return nil, nil, err
	}
	method, err := normalizeRouteMethod(request.Method())
	if err != nil || method == anyMethod {
		return nil, nil, fmt.Errorf("%w: 请求方法 %q 非法", ErrInvalidRoute, request.Method())
	}
	host := normalizeRequestHost(request.Host())
	methods := []string{method}
	methods = append(methods, anyMethod)
	for _, candidateMethod := range methods {
		if matched := ownedMergeStaticMatch(router, candidateMethod, requestParts, trailingSlash, host); matched != nil {
			return matched, nil, nil
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
		if automatic := router.resolveAutoRoute(normalizedPath, requestParts); automatic != nil {
			return automatic, nil, nil
		}
	}
	return nil, nil, nil
}

func ownedMergeStaticMatch(router *Router, method string, requestParts []string, trailingSlash bool, host string) *Route {
	candidates := ownedMergeStaticCandidates(router, method, requestParts)
	for _, exactDomain := range []bool{true, false} {
		if matched := firstMatchingStaticRoute(candidates, host, requestParts, trailingSlash, exactDomain); matched != nil {
			return matched
		}
	}
	return nil
}

func ownedMergeStaticCandidates(router *Router, method string, requestParts []string) []*Route {
	indexed := router.staticRoutes[method][router.pathPartsKey(requestParts)]
	prefix := make([]*Route, 0)
	seen := make(map[*Route]struct{})
	for _, candidate := range router.routes {
		if !isStaticPrefixCandidate(candidate, method, len(requestParts)) {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		prefix = append(prefix, candidate)
	}
	candidates := make([]*Route, 0, len(indexed)+len(prefix))
	candidates = append(candidates, indexed...)
	return append(candidates, prefix...)
}

// BenchmarkStaticPrefixMatch 记录非完整静态匹配修复后的分配和时延，防止只读所有权修复引入热路径退化。
func BenchmarkStaticPrefixMatch(b *testing.B) {
	const prefixRouteCount = 64
	router := NewRouter()
	if err := router.SetCompleteMatch(false); err != nil {
		b.Fatalf("设置非完整匹配失败: %v", err)
	}
	if _, err := router.Get("/catalog/items", "Catalog@Items"); err != nil {
		b.Fatalf("注册精确静态路由失败: %v", err)
	}
	for index := 0; index < prefixRouteCount; index++ {
		if _, err := router.Get(fmt.Sprintf("/prefix-%d", index), "Prefix@Handle"); err != nil {
			b.Fatalf("注册第 %d 条前缀路由失败: %v", index, err)
		}
	}
	if err := router.Freeze(); err != nil {
		b.Fatalf("冻结路由失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/catalog/items", nil))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		matched, _, err := router.Match(request)
		if err != nil || matched == nil {
			b.Fatalf("静态前缀匹配失败，route=%#v err=%v", matched, err)
		}
	}
}
