package route

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestJSONRoutesConsumeTheirDeclaredPath 验证列表不会吞掉详情路径，普通路由继续保留前缀匹配。
func TestJSONRoutesConsumeTheirDeclaredPath(t *testing.T) {
	router := NewRouter()
	if err := router.EnableAutoRoute(false); err != nil {
		t.Fatal(err)
	}
	handler, err := NewJSONHandler(func() string { return "ok" }, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/items", "/items/:id"} {
		if _, err := router.Get(path, handler); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := router.Get("/legacy", func() string { return "legacy" }); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, expected, id string }{
		{"/items", "/items", ""},
		{"/items/42", "/items/:id", "42"},
		{"/items/42/extra", "", ""},
		{"/items.html", "", ""},
		{"/items/name.html", "/items/:id", "name.html"},
		{"/legacy/extra", "/legacy", ""},
	} {
		request := context.MustNewRequest(httptest.NewRequest(http.MethodGet, test.path, nil))
		matched, params, err := router.Match(request)
		if err != nil {
			t.Fatal(err)
		}
		if test.expected == "" {
			if matched != nil {
				t.Fatalf("额外路径匹配了契约: %s => %s", test.path, matched.Path())
			}
			continue
		}
		if matched == nil || matched.Path() != test.expected || test.id != "" && params["id"] != test.id {
			t.Fatalf("路径匹配错误: %s => %#v %#v", test.path, matched, params)
		}
	}
}
