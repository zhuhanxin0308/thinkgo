package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestRootRouteDoesNotConsumeOtherPaths 验证默认前缀匹配下，首页只消费根路径，
// 不会覆盖动态路由、未知路径或显式配置的非根前缀路由。
func TestRootRouteDoesNotConsumeOtherPaths(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			router := NewRouter()
			if err := router.EnableAutoRoute(false); err != nil {
				t.Fatal(err)
			}
			if err := router.SetCompleteMatch(complete); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/", "hello/:name", "docs"} {
				if _, err := router.Rule(path, "Index@Index", method); err != nil {
					t.Fatalf("注册路由 %q 失败: %v", path, err)
				}
			}
			for _, test := range []struct {
				path string
				want string
			}{
				{path: "/", want: "/"},
				{path: "/hello/CLI20260922", want: "/hello/:name"},
				{path: "/hello/CLI20260922.html", want: "/hello/:name"},
				{path: "/unknown"},
				{path: "/docs/guide", want: "/docs"},
			} {
				want := test.want
				if complete && test.path == "/docs/guide" {
					want = ""
				}
				request := fwcontext.MustNewRequest(httptest.NewRequest(method, test.path, nil))
				matched, params, err := router.Match(request)
				if err != nil {
					t.Fatalf("匹配失败: %v", err)
				}
				got := ""
				if matched != nil {
					got = matched.Path()
				}
				if got != want {
					t.Errorf("完整匹配=%t 方法=%s 路径=%q: got=%q want=%q", complete, method, test.path, got, want)
				}
				if want == "/hello/:name" && params["name"] != "CLI20260922" {
					t.Errorf("动态路由参数错误: %#v", params)
				}
			}
		}
	}
}
