package route

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestLiteralSuffixWinsOptionalAliasAtEqualDomainPriority(t *testing.T) {
	for _, pair := range []struct {
		rule, path string
		params     map[string]any
	}{
		{"/index", "/index", nil},
		{"/item/:id/view", "/item/7/view", map[string]any{"id": 7}},
	} {
		t.Run(pair.rule, func(t *testing.T) {
			for _, literalFirst := range []bool{false, true} {
				router := NewRouter()
				var plain, literal *Route
				registerPlain := func() {
					var err error
					plain, err = router.Get(pair.rule, "Page@Plain")
					if err != nil {
						t.Fatal(err)
					}
				}
				registerLiteral := func() {
					var err error
					literal, err = router.Get(pair.rule+".html", "Page@Literal")
					if err != nil {
						t.Fatal(err)
					}
					if err := literal.WithName("literal"); err != nil {
						t.Fatal(err)
					}
				}
				if literalFirst {
					registerLiteral()
					registerPlain()
				} else {
					registerPlain()
					registerLiteral()
				}
				if err := router.Domain("scoped.test", func(group *Group) error {
					_, err := group.Get(pair.rule, "Page@Scoped")
					return err
				}); err != nil {
					t.Fatal(err)
				}
				for _, test := range []struct {
					host, path string
					want       *Route
				}{
					{"example.test", pair.path, plain},
					{"example.test", pair.path + ".html", literal},
					{"example.test", pair.path + ".html.html", nil},
				} {
					request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://"+test.host+test.path, nil))
					got, _ := matchForTest(t, router, request)
					if got != test.want {
						t.Fatalf("字面路径/别名匹配错误: first=%t path=%q got=%v want=%v", literalFirst, test.path, got, test.want)
					}
				}
				request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://scoped.test"+pair.path+".html", nil))
				if got, _ := matchForTest(t, router, request); got == nil || got.domain != "scoped.test" {
					t.Fatalf("精确域名优先规则发生变化: %v", got)
				}
				if url, err := router.URL("literal", pair.params); err != nil || url != pair.path+".html" {
					t.Fatalf("命名路由重复添加后缀: %q, %v", url, err)
				}
			}
		})
	}
}

func TestExplicitExtensionAlreadyInLiteralIsNotDuplicated(t *testing.T) {
	router := NewRouter()
	literal, err := router.Get("/legacy.html", "Page@Literal")
	if err != nil {
		t.Fatal(err)
	}
	if err := literal.WithExtension("html"); err != nil {
		t.Fatal(err)
	}
	if err := literal.WithName("legacy"); err != nil {
		t.Fatal(err)
	}
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test/legacy.html", nil))
	if got, _ := matchForTest(t, router, req); got != literal {
		t.Fatalf("带显式扩展名的字面路由不可达: %v", got)
	}
	if url, err := router.URL("legacy", nil); err != nil || url != "/legacy.html" {
		t.Fatalf("显式后缀应只出现一次: %q, %v", url, err)
	}
}

// TestLiteralSuffixKeepsEquivalentRegistrationOrder 验证两个字面候选继续按注册顺序匹配。
func TestLiteralSuffixKeepsEquivalentRegistrationOrder(t *testing.T) {
	for _, pair := range []struct{ rule, path string }{
		{"/index", "/index"},
		{"/item/:id/view", "/item/7/view"},
	} {
		t.Run(pair.rule, func(t *testing.T) {
			router := NewRouter()
			handler, err := NewJSONHandler(func() string { return "first" }, http.StatusOK)
			if err != nil {
				t.Fatal(err)
			}
			first, err := router.Get(pair.rule+".html", handler)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := router.Get(pair.rule+".HTML", "Page@Other"); err != nil {
				t.Fatal(err)
			}
			req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+pair.path+".html", nil))
			if got, _ := matchForTest(t, router, req); got != first {
				t.Fatalf("等价字面路由应遵循注册顺序: %v", got)
			}
		})
	}
}

// TestLiteralExtensionKeepsRequiredRequestSuffix 验证前缀匹配仍校验整个请求的强制后缀。
func TestLiteralExtensionKeepsRequiredRequestSuffix(t *testing.T) {
	for _, pair := range []struct{ rule, path string }{
		{"/legacy.html", "/legacy.html"},
		{"/item/:id?/view.html", "/item/view.html"},
	} {
		t.Run(pair.rule, func(t *testing.T) {
			router := NewRouter()
			registered, err := router.Get(pair.rule, "Page@Literal")
			if err != nil {
				t.Fatal(err)
			}
			if err := registered.WithExtension("html"); err != nil {
				t.Fatal(err)
			}
			for suffix, want := range map[string]bool{"": true, "/extra": false, "/extra.html": true} {
				req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+pair.path+suffix, nil))
				if got, _ := matchForTest(t, router, req); (got == registered) != want {
					t.Errorf("路径 %q 的匹配应为 %t，实际为 %v", pair.path+suffix, want, got)
				}
			}
		})
	}
}

func TestNamedRouteCacheNormalizesExistingLiteralSuffix(t *testing.T) {
	router := NewRouter()
	content, err := json.Marshal(namedRouteCache{
		Version: nameCacheVersion, DefaultPattern: router.defaultPattern,
		Entries: []namedRouteEntry{{Name: "legacy", Path: "/legacy.html", Extension: "html", ExtensionRequired: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.LoadNameCache(content); err != nil {
		t.Fatal(err)
	}
	if url, err := router.URL("legacy", nil); err != nil || url != "/legacy.html" {
		t.Fatalf("升级后旧缓存不应生成两次后缀: %q, %v", url, err)
	}
}

// TestNamedRouteCacheUsesCurrentCaseSensitivity 验证加载缓存后仍按当前大小写策略生成 URL。
func TestNamedRouteCacheUsesCurrentCaseSensitivity(t *testing.T) {
	router := NewRouter()
	content, err := json.Marshal(namedRouteCache{
		Version: nameCacheVersion, DefaultPattern: router.defaultPattern,
		Entries: []namedRouteEntry{{Name: "legacy", Path: "/legacy.HTML", Extension: "html", ExtensionRequired: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.LoadNameCache(content); err != nil {
		t.Fatal(err)
	}
	if url, err := router.URL("legacy", nil); err != nil || url != "/legacy.HTML" {
		t.Fatalf("大小写不敏感时应只生成一次后缀: %q, %v", url, err)
	}
	if err := router.SetCaseSensitive(true); err != nil {
		t.Fatal(err)
	}
	registered, err := router.Get("/legacy.HTML", "Page@Literal")
	if err != nil {
		t.Fatal(err)
	}
	if err := registered.WithExtension("html"); err != nil {
		t.Fatal(err)
	}
	url, err := router.URL("legacy", nil)
	if err != nil || url != "/legacy.HTML.html" {
		t.Fatalf("大小写敏感时必须追加显式的小写后缀: %q, %v", url, err)
	}
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+url, nil))
	if got, _ := matchForTest(t, router, req); got != registered {
		t.Fatalf("缓存返回的地址应可被已注册路由匹配: %v", got)
	}
}
