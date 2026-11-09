package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestDynamicRouteUnicodeFold 验证索引与线性匹配共享 Unicode 折叠语义，并保留参数原文。
func TestDynamicRouteUnicodeFold(t *testing.T) {
	for _, pair := range [][2]string{{"s", "ſ"}, {"σ", "ς"}, {"K", "K"}} {
		for _, sensitive := range []bool{false, true} {
			for _, complete := range []bool{false, true} {
				for _, domain := range []string{"", "api.example.com"} {
					t.Run(fmt.Sprintf("%s/%t/%t/%s", pair[0], sensitive, complete, domain), func(t *testing.T) {
						router := NewRouter()
						for _, err := range []error{router.EnableAutoRoute(false), router.SetCaseSensitive(sensitive), router.SetCompleteMatch(complete)} {
							if err != nil {
								t.Fatal(err)
							}
						}
						register := func(group *Group) error {
							item, err := group.Get("/"+pair[0]+"/:id?/"+pair[0], "Fold@Show")
							if err != nil {
								return err
							}
							return item.WithExtension("json")
						}
						var err error
						if domain == "" {
							err = register(router.rootGroup())
						} else {
							err = router.Domain(domain, register)
						}
						if err != nil {
							t.Fatal(err)
						}
						for _, middle := range []string{"/MiXeD", ""} {
							for _, suffix := range []string{".json", "/extra.json", ".JSON", ""} {
								path := "/" + pair[1] + middle + "/" + pair[1] + suffix
								request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://api.example.com"+path, nil))
								actual, params, actualErr := router.Match(request)
								expected, expectedParams, expectedErr := linearRouteMatch(router, request)
								assertRouteMatchParity(t, actual, params, actualErr, expected, expectedParams, expectedErr)
								wantMatch := !sensitive && (suffix == ".json" || suffix == "/extra.json" && !complete)
								if (actual != nil) != wantMatch {
									t.Fatalf("路径 %q 匹配结果错误: 路由=%v, 预期命中=%t", path, actual, wantMatch)
								}
								if actual != nil && middle != "" && params["id"] != "MiXeD" {
									t.Fatalf("参数原文被修改: %v", params)
								}
							}
						}
					})
				}
			}
		}
	}
}

// TestDynamicIndexAllUnicodeFoldClasses 检查所有非平凡折叠类，避免仅为几个示例修补索引。
func TestDynamicIndexAllUnicodeFoldClasses(t *testing.T) {
	router := NewRouter()
	for value := rune(0); value <= unicode.MaxRune; value++ {
		next := unicode.SimpleFold(value)
		if next == value {
			continue
		}
		literal := string(value)
		item := &Route{router: router, pathParts: []routePart{{literal: literal}, {param: "id"}}}
		index := buildRouteIndex(map[string][]*Route{http.MethodGet: {item}})
		candidates := index.candidates(http.MethodGet, "", []string{string(next), "42"}, false)
		if len(candidates) != 1 || candidates[0] != item {
			t.Fatalf("简单折叠类 U+%04X -> U+%04X 被动态索引遗漏", value, next)
		}
	}
}

// TestNameCacheInvalidExtensionPreservesSnapshot 确保放宽空后缀不会放行损坏缓存或污染已加载快照。
func TestNameCacheInvalidExtensionPreservesSnapshot(t *testing.T) {
	router := NewRouter()
	valid := []byte(`{"version":1,"default_pattern":"[0-9]+","entries":[{"name":"kept","path":"/kept"}]}`)
	if err := router.LoadNameCache(valid); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"extension":"../json"`, `"extension":"."`, `"extension_required":true`} {
		content := []byte(`{"version":1,"default_pattern":"[0-9]+","entries":[{"name":"bad","path":"/bad",` + field + `}]}`)
		if err := router.LoadNameCache(content); err == nil {
			t.Fatalf("非法后缀元数据未被拒绝: %s", content)
		}
		if got, err := router.URL("kept", nil); err != nil || got != "/kept" {
			t.Fatalf("错误加载污染已有快照: %q, %v", got, err)
		}
	}
	item, err := router.Get("/explicit", "Explicit@Show")
	if err != nil {
		t.Fatal(err)
	}
	if err := item.WithExtension(""); err == nil {
		t.Fatal("显式空后缀仍应拒绝")
	}
}

// TestRootRouteURLRoundTrip 同时校验根路径的正向匹配、反向 URL 和命名缓存恢复。
func TestRootRouteURLRoundTrip(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, required := range []bool{false, true} {
			t.Run(fmt.Sprintf("完整=%t/强制后缀=%t", complete, required), func(t *testing.T) {
				router := NewRouter()
				for _, err := range []error{router.SetCompleteMatch(complete), router.EnableAutoRoute(false)} {
					if err != nil {
						t.Fatal(err)
					}
				}
				root, err := router.Get("/", "Root@Index")
				if err != nil {
					t.Fatal(err)
				}
				if err := root.WithName("root.index"); err != nil {
					t.Fatal(err)
				}
				want := "/"
				if required {
					if err := root.WithExtension("json"); err != nil {
						t.Fatal(err)
					}
					want = "/.json"
				}
				path, err := router.URL("root.index", nil)
				if err != nil || path != want {
					t.Fatalf("根路由 URL=%q，预期=%q，错误=%v", path, want, err)
				}
				request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil))
				if matched, _, err := router.Match(request); err != nil || matched != root {
					t.Fatalf("生成的根 URL 无法匹配原路由: %v, %v", matched, err)
				}
				content, err := router.ExportNameCache()
				if err != nil {
					t.Fatal(err)
				}
				restored := NewRouter()
				if err := restored.LoadNameCache(content); err != nil {
					t.Fatal(err)
				}
				if path, err := restored.URL("root.index", nil); err != nil || path != want {
					t.Fatalf("缓存恢复改变根 URL: %q, %v", path, err)
				}
			})
		}
	}
}

// TestNameCacheMixedExtensions 覆盖 JSON 契约、关闭默认后缀及混合条目的导入导出。
func TestNameCacheMixedExtensions(t *testing.T) {
	for _, defaultExtension := range []string{"", "html"} {
		t.Run("默认后缀="+defaultExtension, func(t *testing.T) {
			router := NewRouter()
			if err := router.SetDefaultExtension(defaultExtension); err != nil {
				t.Fatal(err)
			}
			jsonHandler, err := NewJSONHandler(func() string { return "ok" }, http.StatusOK)
			if err != nil {
				t.Fatal(err)
			}
			for _, definition := range []struct {
				name    string
				handler any
				ext     string
			}{{"plain", "Plain@Show", ""}, {"json", jsonHandler, ""}, {"explicit", "Explicit@Show", "json"}} {
				item, err := router.Get("/"+definition.name+"/:id", definition.handler)
				if err != nil {
					t.Fatal(err)
				}
				if err := item.WithName(definition.name); err != nil {
					t.Fatal(err)
				}
				if definition.ext != "" {
					if err := item.WithExtension(definition.ext); err != nil {
						t.Fatal(err)
					}
				}
			}
			content, err := router.ExportNameCache()
			if err != nil {
				t.Fatal(err)
			}
			restored := NewRouter()
			if err := restored.LoadNameCache(content); err != nil {
				t.Fatalf("合法混合缓存无法加载: %v", err)
			}
			for _, name := range []string{"plain", "json", "explicit"} {
				params := map[string]interface{}{"id": 42}
				want, err := router.URL(name, params)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := restored.URL(name, params); err != nil || got != want {
					t.Fatalf("缓存 URL 不一致: %q，预期=%q，错误=%v", got, want, err)
				}
			}
		})
	}
}
