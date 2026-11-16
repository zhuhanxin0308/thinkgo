package route

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestExtensionAliasIgnoresUnconsumedSuffix 验证未消费的请求尾段不会改变前缀路由顺序。
func TestExtensionAliasIgnoresUnconsumedSuffix(t *testing.T) {
	for _, test := range []struct{ rule, path string }{
		{"/files", "/files/detail.json"},
		{"/files/:id", "/files/5/detail.json"},
		{"/files/:id?/view", "/files/view/detail.json"},
	} {
		t.Run(test.rule, func(t *testing.T) {
			router := NewRouter()
			first, err := router.Get(test.rule, "Files@First")
			if err != nil {
				t.Fatal(err)
			}
			if err := first.WithExtension("json"); err != nil {
				t.Fatal(err)
			}
			if _, err := router.Get(test.rule, "Files@Second"); err != nil {
				t.Fatal(err)
			}
			req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+test.path, nil))
			if got, _ := matchForTest(t, router, req); got != first {
				t.Fatalf("相同前缀应遵守注册顺序: %v", got)
			}
		})
	}
}

// TestDynamicAliasPreservesOrderForParameterOnlyMatches 验证没有字面后缀的参数候选不改变顺序。
func TestDynamicAliasPreservesOrderForParameterOnlyMatches(t *testing.T) {
	router := NewRouter()
	first, err := router.Get("/items/:id", "Items@First")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewJSONHandler(func() string { return "second" }, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/items/:name", handler); err != nil {
		t.Fatal(err)
	}
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test/items/7.html", nil))
	if got, params := matchForTest(t, router, req); got != first || params["id"] != "7" {
		t.Fatalf("同优先级的参数路由应遵守注册顺序，参数为 %v", params)
	}
}

// TestDynamicSuffixOnlySegmentPrefersLiteral 验证扩展名独占一个请求段时也优先匹配字面路径。
func TestDynamicSuffixOnlySegmentPrefersLiteral(t *testing.T) {
	for _, rules := range [][]string{
		{"/item/:id?/view", "/item/:id?/.html"},
		{"/item/:id?/.html", "/item/:id?/view"},
	} {
		router := NewRouter()
		if err := router.SetCompleteMatch(true); err != nil {
			t.Fatal(err)
		}
		for _, rule := range rules {
			if _, err := router.Get(rule, "Items@Show"); err != nil {
				t.Fatal(err)
			}
		}
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test/item/view/.html", nil))
		got, params := matchForTest(t, router, request)
		if got == nil || got.path != "/item/:id?/.html" || params["id"] != "view" {
			t.Fatalf("字面扩展名路径应优先匹配: %v, %v", got, params)
		}
	}
}

// TestLiteralExtensionKeepsRequiredRequestSuffix 验证前缀匹配仍校验整个请求的强制后缀。
func TestLiteralExtensionKeepsRequiredRequestSuffix(t *testing.T) {
	for _, pair := range []struct{ rule, path string }{
		{"/legacy.html", "/legacy.html"},
		{"/item/:id?/view.html", "/item/view.html"},
		{"/mandatory/:id/view.html", "/mandatory/7/view.html"},
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
			if !strings.Contains(pair.rule, "?") {
				uppercaseLiteral := strings.TrimSuffix(pair.path, ".html") + ".HTML"
				uppercaseTail := uppercaseLiteral[strings.LastIndexByte(uppercaseLiteral, '/'):]
				for requestPath, want := range map[string]bool{uppercaseLiteral: true, pair.path + uppercaseTail: false} {
					req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.test"+requestPath, nil))
					if got, _ := matchForTest(t, router, req); (got == registered) != want {
						t.Errorf("路径 %q 的字面后缀豁免范围错误: %v", requestPath, got)
					}
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
