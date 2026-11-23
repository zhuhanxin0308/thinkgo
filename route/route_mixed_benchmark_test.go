package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

const mixedBenchmarkDynamicRouteCount = 1000

func TestStaticPrefixIndexPreservesRequiredExtensionAndExactMatch(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(strconv.FormatBool(complete), func(t *testing.T) {
			router := NewRouter()
			if err := router.EnableAutoRoute(false); err != nil {
				t.Fatal(err)
			}
			if err := router.SetCompleteMatch(complete); err != nil {
				t.Fatal(err)
			}
			broad, err := router.Get("/api", "API@Root")
			if err != nil {
				t.Fatal(err)
			}
			detailed, err := router.Get("/api/list", "API@List")
			if err != nil {
				t.Fatal(err)
			}
			if err := detailed.WithExtension("json"); err != nil {
				t.Fatal(err)
			}
			literal, err := router.Get("/api/list.json", "API@LiteralList")
			if err != nil {
				t.Fatal(err)
			}
			if err := literal.WithExtension("json"); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				path       string
				want       *Route
				prefixOnly bool
			}{
				{path: "/api/list/.json", want: detailed},
				{path: "/api/list.json", want: literal},
				{path: "/api/list/page.json", want: detailed, prefixOnly: true},
				{path: "/api/list/page", want: broad, prefixOnly: true},
				{path: "/api/list.json/page.json", want: literal, prefixOnly: true},
				{path: "/api/list.json/page", want: broad, prefixOnly: true},
			} {
				want := test.want
				if complete && test.prefixOnly {
					want = nil
				}
				request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+test.path, nil))
				matched, _, err := router.Match(request)
				if err != nil || matched != want {
					t.Errorf("路径 %q 匹配错误: %v", test.path, err)
				}
			}
		})
	}
}

// BenchmarkMixedDefaultRouteMatch 覆盖静态和动态路由混合注册时的常见命中及未命中。
func BenchmarkMixedDefaultRouteMatch(b *testing.B) {
	for _, count := range []int{mixedBenchmarkDynamicRouteCount, 10000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			router, cases := mixedDefaultBenchmarkRoutes(b, count)
			for _, test := range cases {
				b.Run(test.name, func(b *testing.B) {
					request := test.request()
					b.ReportAllocs()
					for b.Loop() {
						matched, _, err := router.Match(request)
						if err != nil || (matched != nil) != test.found {
							b.Fatalf("匹配结果与基准场景不符: %v，%v", matched, err)
						}
					}
				})
			}
		})
	}
}

// BenchmarkFrozenRouterParallel 测量共享已冻结路由器的闭环匹配，工作协程数由 -cpu 指定。
func BenchmarkFrozenRouterParallel(b *testing.B) {
	router, cases := mixedDefaultBenchmarkRoutes(b, mixedBenchmarkDynamicRouteCount)
	b.Run("Freeze", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if err := router.Freeze(); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			// 每个工作协程独享一个预先构造的请求，只共享待测路由器。
			requests := make([]*fwcontext.Request, runtime.GOMAXPROCS(0))
			for i := range requests {
				requests[i] = test.request()
			}
			var worker atomic.Int32
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				request := requests[worker.Add(1)-1]
				for pb.Next() {
					matched, _, err := router.Match(request)
					if err != nil || (matched != nil) != test.found {
						b.Errorf("匹配结果不符，路径=%s err=%v", test.path, err)
						return
					}
				}
			})
		})
	}
}

type routeMatchBenchmarkCase struct {
	name, path string
	found      bool
}

func (test routeMatchBenchmarkCase) request() *fwcontext.Request {
	return fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+test.path, nil))
}

// 串行和并发基准使用相同的已冻结路由表及请求分布。
func mixedDefaultBenchmarkRoutes(b *testing.B, count int) (*Router, []routeMatchBenchmarkCase) {
	b.Helper()
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		b.Fatal(err)
	}
	if err := router.SetCompleteMatch(false); err != nil {
		b.Fatal(err)
	}
	if _, err := router.Get("/health", "Health@Check"); err != nil {
		b.Fatal(err)
	}
	for index := range count {
		if _, err := router.Get(fmt.Sprintf("/resource/%d/:value", index), "Resource@Show"); err != nil {
			b.Fatal(err)
		}
	}
	if err := router.Freeze(); err != nil {
		b.Fatal(err)
	}
	return router, []routeMatchBenchmarkCase{
		{name: "Static", path: "/health", found: true},
		{name: "Dynamic", path: fmt.Sprintf("/resource/%d/value", count-1), found: true},
		{name: "Miss", path: "/not-registered"},
	}
}
