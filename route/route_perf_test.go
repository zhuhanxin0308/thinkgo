package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestDynamicRoutesIndexedByMethod 验证冻结后只扫描当前方法和通配方法的动态路由。
func TestDynamicRoutesIndexedByMethod(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/api/users/:id", "User@Show"); err != nil {
		t.Fatalf("注册 GET 动态路由失败: %v", err)
	}
	if _, err := router.Post("/api/users/:id", "User@Update"); err != nil {
		t.Fatalf("注册 POST 动态路由失败: %v", err)
	}
	if _, err := router.Any("/api/common/:name", "Common@Handle"); err != nil {
		t.Fatalf("注册 ANY 动态路由失败: %v", err)
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结路由失败: %v", err)
	}

	if len(router.dynamicRoutes[http.MethodGet]) != 1 {
		t.Fatalf("GET 动态路由索引数量应为 1，实际为 %d", len(router.dynamicRoutes[http.MethodGet]))
	}
	if len(router.dynamicRoutes[http.MethodPost]) != 1 {
		t.Fatalf("POST 动态路由索引数量应为 1，实际为 %d", len(router.dynamicRoutes[http.MethodPost]))
	}
	if len(router.dynamicRoutes[anyMethod]) != 1 {
		t.Fatalf("ANY 动态路由索引数量应为 1，实际为 %d", len(router.dynamicRoutes[anyMethod]))
	}
}

// TestDefaultExtensionDynamicRouteCandidatesStayBounded 验证默认可选后缀不会让
// 动态路由退回全量扫描；候选规模必须只由实际路径冲突决定，而不能随注册总量增长。
func TestDefaultExtensionDynamicRouteCandidatesStayBounded(t *testing.T) {
	for _, routeCount := range []int{32, 4096} {
		t.Run(strconv.Itoa(routeCount), func(t *testing.T) {
			router := NewRouter()
			if err := router.EnableAutoRoute(false); err != nil {
				t.Fatalf("关闭自动路由失败: %v", err)
			}
			if err := router.SetCompleteMatch(true); err != nil {
				t.Fatalf("启用完整匹配失败: %v", err)
			}
			var expected *Route
			for index := 0; index < routeCount; index++ {
				registered, err := router.Get(fmt.Sprintf("/resource/%d/:value/detail", index), "Resource@Show")
				if err != nil {
					t.Fatalf("注册第 %d 条动态路由失败: %v", index, err)
				}
				if index == routeCount-1 {
					expected = registered
				}
			}
			if err := router.Freeze(); err != nil {
				t.Fatalf("冻结动态路由失败: %v", err)
			}

			paths := []struct {
				path string
				want *Route
			}{
				{path: fmt.Sprintf("/resource/%d/value/detail", routeCount-1), want: expected},
				{path: fmt.Sprintf("/resource/%d/value/detail.html", routeCount-1), want: expected},
				{path: fmt.Sprintf("/resource/%d/value/detail.html", routeCount)},
			}
			for _, testCase := range paths {
				request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+testCase.path, nil))
				parts, _, _, err := requestPathPartsWithOptions(request, router.removeSlash)
				if err != nil {
					t.Fatalf("解析请求路径 %q 失败: %v", testCase.path, err)
				}
				candidates := router.dynamicIndex.candidates(http.MethodGet, "example.com", parts, false)
				if testCase.want == nil {
					if len(candidates) != 0 {
						t.Fatalf("路由总量为 %d 时未命中路径 %q 不应返回候选，实际候选数=%d", routeCount, testCase.path, len(candidates))
					}
					continue
				}
				if len(candidates) != 1 || candidates[0] != testCase.want {
					t.Fatalf("路由总量为 %d 时路径 %q 应只返回目标候选，实际候选数=%d", routeCount, testCase.path, len(candidates))
				}
			}
		})
	}
}

// TestRouterConcurrentRegistrationAndMatching 验证无共享分组状态时并发注册安全，冻结后可并发只读匹配。
func TestRouterConcurrentRegistrationAndMatching(t *testing.T) {
	router := NewRouter()
	const routeCount = 64
	errorsChannel := make(chan error, routeCount)
	var registrations sync.WaitGroup
	for index := 0; index < routeCount; index++ {
		index := index
		registrations.Add(1)
		go func() {
			defer registrations.Done()
			_, err := router.Get(fmt.Sprintf("/items/%d", index), "Item@Show")
			errorsChannel <- err
		}()
	}
	registrations.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("并发注册路由失败: %v", err)
		}
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结并发注册结果失败: %v", err)
	}

	var matches sync.WaitGroup
	for index := 0; index < routeCount; index++ {
		index := index
		matches.Add(1)
		go func() {
			defer matches.Done()
			req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://example.com/items/%d", index), nil))
			matched, _, err := router.Match(req)
			if err != nil || matched == nil {
				t.Errorf("并发匹配路由 %d 失败，路由=%#v 错误=%v", index, matched, err)
			}
		}()
	}
	matches.Wait()
}

// BenchmarkRouteMatch 记录动态路由在不同规模、首中尾命中和未命中场景下的匹配成本。
func BenchmarkRouteMatch(b *testing.B) {
	for _, routeCount := range []int{10, 100, 1000, 10000} {
		b.Run(strconv.Itoa(routeCount), func(b *testing.B) {
			router := benchmarkRouter(b, routeCount)
			requests := make([]*fwcontext.Request, 0, 4)
			for _, routeIndex := range []int{0, routeCount / 2, routeCount - 1, routeCount} {
				raw := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://example.com/resource/%d/value", routeIndex), nil)
				requests = append(requests, fwcontext.MustNewRequest(raw))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				matched, _, err := router.Match(requests[index%len(requests)])
				if err != nil {
					b.Fatal(err)
				}
				if index%len(requests) == len(requests)-1 {
					if matched != nil {
						b.Fatal("未注册的动态路由不应命中")
					}
				} else if matched == nil {
					b.Fatal("已注册的动态路由必须命中")
				}
			}
		})
	}
}

// BenchmarkRouteMatchLiteralExtension 记录后缀附着在动态路由末尾字面量时，
// Trie 经过后缀投影后的规模趋势，防止后续实现重新退化为全量候选扫描。
func BenchmarkRouteMatchLiteralExtension(b *testing.B) {
	for _, routeCount := range []int{10, 100, 1000, 10000} {
		b.Run(strconv.Itoa(routeCount), func(b *testing.B) {
			router := NewRouter()
			if err := router.EnableAutoRoute(false); err != nil {
				b.Fatalf("关闭自动路由失败: %v", err)
			}
			if err := router.SetCompleteMatch(true); err != nil {
				b.Fatalf("启用完整匹配失败: %v", err)
			}
			for index := 0; index < routeCount; index++ {
				if _, err := router.Get(fmt.Sprintf("/resource/%d/:value/detail", index), "Resource@Show"); err != nil {
					b.Fatalf("注册基准路由 %d 失败: %v", index, err)
				}
			}
			if err := router.Freeze(); err != nil {
				b.Fatalf("冻结基准路由失败: %v", err)
			}
			request := fwcontext.MustNewRequest(httptest.NewRequest(
				http.MethodGet,
				fmt.Sprintf("http://example.com/resource/%d/value/detail.html", routeCount-1),
				nil,
			))
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				matched, _, err := router.Match(request)
				if err != nil || matched == nil {
					b.Fatalf("后缀字面量路由匹配失败，route=%#v err=%v", matched, err)
				}
			}
		})
	}
}

func benchmarkRouter(b *testing.B, routeCount int) *Router {
	b.Helper()
	router := NewRouter()
	for index := 0; index < routeCount; index++ {
		if _, err := router.Get(fmt.Sprintf("/resource/%d/:value", index), "Resource@Show"); err != nil {
			b.Fatalf("注册基准路由 %d 失败: %v", index, err)
		}
	}
	if err := router.Freeze(); err != nil {
		b.Fatalf("冻结基准路由失败: %v", err)
	}
	return router
}
