package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

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
			for _, test := range []struct {
				path       string
				want       *Route
				prefixOnly bool
			}{
				{path: "/api/list/.json", want: detailed},
				{path: "/api/list.json", want: detailed},
				{path: "/api/list/page.json", want: detailed, prefixOnly: true},
				{path: "/api/list/page", want: broad, prefixOnly: true},
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
	for _, count := range []int{1000, 10000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
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
			for _, test := range []struct {
				name, path string
				found      bool
			}{
				{name: "Static", path: "/health", found: true},
				{name: "Dynamic", path: fmt.Sprintf("/resource/%d/value", count-1), found: true},
				{name: "Miss", path: "/not-registered"},
			} {
				b.Run(test.name, func(b *testing.B) {
					request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+test.path, nil))
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
