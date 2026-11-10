package route

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestRoutePatternsAreIsolatedExplicitMetadata(t *testing.T) {
	router := NewRouter()
	if err := router.SetDefaultPattern("[a-z0-9]+"); err != nil {
		t.Fatal(err)
	}
	registered, err := router.Get("/items/:id/:slug", "Item/show")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"id": "[0-9]+", "slug": "[a-z]+"}
	for name, pattern := range want {
		if err := registered.WithPattern(name, pattern); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := router.Get("/default/:id", "Default/show"); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/static", "Static/show"); err != nil {
		t.Fatal(err)
	}
	routes, err := router.Routes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(routes[0].Patterns, want) || len(routes[1].Patterns) != 0 || len(routes[2].Patterns) != 0 {
		t.Fatalf("显式与默认约束元数据混淆: %+v", routes)
	}
	routes[0].Patterns["id"] = ".*"
	delete(routes[0].Patterns, "slug")
	next, err := router.Routes()
	if err != nil || !reflect.DeepEqual(next[0].Patterns, want) {
		t.Fatalf("快照改写了内部约束: %+v %v", next, err)
	}
	for _, test := range []struct {
		path string
		want bool
	}{{"/items/12/name", true}, {"/items/wrong/name", false}, {"/items/12/34", false}} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+test.path, nil))
		matched, _ := matchForTest(t, router, request)
		if (matched != nil) != test.want {
			t.Fatalf("元数据修改污染了冻结匹配: %s", test.path)
		}
	}
}
