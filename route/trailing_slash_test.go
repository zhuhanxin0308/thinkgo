package route

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestTrailingSlashOptionalRootURL 验证省略全部可选段时不会生成属于另一条路由的根路径。
func TestTrailingSlashOptionalRootURL(t *testing.T) {
	for _, relaxed := range []bool{false, true} {
		router := NewRouter()
		if err := router.SetRemoveSlash(relaxed); err != nil {
			t.Fatal(err)
		}
		if err := router.SetDefaultExtension(""); err != nil {
			t.Fatal(err)
		}
		registered, err := router.Get("/:id?/", "Page@Show")
		if err != nil {
			t.Fatal(err)
		}
		if err := registered.WithName("page"); err != nil {
			t.Fatal(err)
		}
		cache, err := router.ExportNameCache()
		if err != nil {
			t.Fatal(err)
		}
		restored := NewRouter()
		if err := restored.SetRemoveSlash(relaxed); err != nil {
			t.Fatal(err)
		}
		if err := restored.LoadNameCache(cache); err != nil {
			t.Fatal(err)
		}
		for _, source := range []*Router{router, restored} {
			path, err := source.URL("page", nil)
			if !relaxed {
				if !errors.Is(err, ErrInvalidRouteParameter) || path != "" {
					t.Errorf("严格模式不能用根路径表示尾斜杠: path=%q err=%v", path, err)
				}
			} else if err != nil || path != "/" {
				t.Errorf("忽略尾斜杠时允许省略可选段: path=%q err=%v", path, err)
			}
			path, err = source.URL("page", map[string]interface{}{"id": "docs"})
			if err != nil {
				t.Fatal(err)
			}
			hit, params := matchForTest(t, router, fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, path, nil)))
			if hit != registered || params["id"] != "docs" {
				t.Fatalf("提供可选段后 URL 未回环: path=%q params=%v", path, params)
			}
		}
	}
}

// TestTrailingSlashPathByteLimit 验证模板、命名缓存与实际生成路径共享请求端的字节上限。
func TestTrailingSlashPathByteLimit(t *testing.T) {
	router := NewRouter()
	if err := router.SetDefaultExtension(""); err != nil {
		t.Fatal(err)
	}
	atLimit := "/" + strings.Repeat("a", maxRoutePathLength-len("//")) + "/"
	overLimit := "/a" + atLimit[1:]
	if _, err := router.Get(overLimit, "Docs@Large"); !errors.Is(err, ErrInvalidRoute) {
		t.Errorf("超限模板应在注册时拒绝: %v", err)
	}
	if _, err := router.Get(atLimit, "Docs@Limit"); err != nil {
		t.Fatal(err)
	}
	named, err := router.Get("/:id/", "Docs@Dynamic")
	if err != nil {
		t.Fatal(err)
	}
	if err := named.WithName("docs"); err != nil {
		t.Fatal(err)
	}
	if err := named.WithPattern("id", ".+"); err != nil {
		t.Fatal(err)
	}
	// 转义后的路径字节数才是请求端实际检查的长度；普通查询参数不计入路径上限。
	encodedSegment := strings.Repeat(" ", (maxRoutePathLength-len("//"))/len("%20"))
	if path, err := router.URL("docs", map[string]interface{}{"id": encodedSegment, "q": "test"}); err != nil || len(path) != maxRoutePathLength+len("?q=test") {
		t.Fatalf("到达上限且附带查询的 URL 应有效: pathBytes=%d err=%v", len(path), err)
	}
	if path, err := router.URL("docs", map[string]interface{}{"id": encodedSegment + "a"}); !errors.Is(err, ErrInvalidRouteParameter) || path != "" {
		t.Errorf("动态参数转义后越界应返回参数错误: pathBytes=%d err=%v", len(path), err)
	}
	if matched, _ := matchForTest(t, router, fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, atLimit, nil))); matched == nil || matched.Handler() != "Docs@Limit" {
		t.Fatal("恰好达到上限的静态路径未命中")
	}
	restored := NewRouter()
	if err := restored.LoadNameCache([]byte(`{"version":1,"default_pattern":"[a-z]+","entries":[{"name":"docs","path":"` + overLimit + `"}]}`)); !errors.Is(err, ErrInvalidRoute) {
		t.Errorf("超限缓存条目应被拒绝: %v", err)
	}
}

// TestExplicitTrailingSlashRoutesMatchAndCoexist 验证默认严格斜杠模式下显式尾斜杠路由能正确命中，
// 且与同名无尾斜杠路由互不冲突，并保持 Route.Path、Routes 与 AddWithCommit 元数据一致。
func TestExplicitTrailingSlashRoutesMatchAndCoexist(t *testing.T) {
	router := NewRouter()
	if err := router.SetDefaultExtension(""); err != nil {
		t.Fatal(err)
	}
	if err := router.SetCompleteMatch(true); err != nil {
		t.Fatal(err)
	}
	plain, err := router.Get("/docs", "Docs@Plain")
	if err != nil {
		t.Fatalf("注册无尾斜杠路由失败: %v", err)
	}
	slashed, err := router.Get("/docs/", "Docs@Directory")
	if err != nil {
		t.Fatalf("严格斜杠模式下应允许 /docs 与 /docs/ 共存: %v", err)
	}
	if plain.Path() != "/docs" || slashed.Path() != "/docs/" {
		t.Fatalf("Route.Path 应保留显式尾斜杠: plain=%q slashed=%q", plain.Path(), slashed.Path())
	}

	var committedPath string
	if _, err := router.AddWithCommit(http.MethodGet, "/portal/", "Portal@Index", func(info RouteInfo) error {
		committedPath = info.Path
		return nil
	}); err != nil {
		t.Fatalf("AddWithCommit 注册尾斜杠路由失败: %v", err)
	}
	if committedPath != "/portal/" {
		t.Fatalf("AddWithCommit 收到的 RouteInfo.Path 丢失尾斜杠: %q", committedPath)
	}

	if err := router.Group("/api", func(api *Group) error {
		if _, err := api.Get("", "Api@Root"); err != nil {
			return err
		}
		if _, err := api.Get("/", "Api@RootSlash"); err != nil {
			return err
		}
		if _, err := api.Get("/users/:id/", "User@DetailSlash"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("分组注册尾斜杠路由失败: %v", err)
	}

	cases := []struct {
		path        string
		wantHandler string
		wantParamID string
	}{
		{"/docs", "Docs@Plain", ""},
		{"/docs/", "Docs@Directory", ""},
		{"/portal/", "Portal@Index", ""},
		{"/portal", "", ""},
		{"/api", "Api@Root", ""},
		{"/api/", "Api@RootSlash", ""},
		{"/api/users/42/", "User@DetailSlash", "42"},
		{"/api/users/42", "", ""},
	}
	for _, tc := range cases {
		req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+tc.path, nil))
		matched, params, matchErr := router.Match(req)
		if matchErr != nil {
			t.Fatalf("匹配 %q 返回意外错误: %v", tc.path, matchErr)
		}
		if tc.wantHandler == "" {
			if matched != nil {
				t.Fatalf("路径 %q 不应命中任何路由，实际命中 %v (%s)", tc.path, matched.Handler(), matched.Path())
			}
			continue
		}
		if matched == nil || matched.Handler() != tc.wantHandler {
			t.Fatalf("路径 %q 命中错误: matched=%#v want=%s", tc.path, matched, tc.wantHandler)
		}
		if tc.wantParamID != "" && params["id"] != tc.wantParamID {
			t.Fatalf("路径 %q 参数提取错误: got=%#v want id=%s", tc.path, params, tc.wantParamID)
		}
	}

	snapshot, err := router.Routes()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(snapshot))
	for _, info := range snapshot {
		seen[info.Path] = true
	}
	for _, expected := range []string{"/docs", "/docs/", "/portal/", "/api", "/api/", "/api/users/:id/"} {
		if !seen[expected] {
			t.Fatalf("Routes 快照缺少路径 %q，实际快照=%#v", expected, seen)
		}
	}
}

// TestSetRemoveSlashAndCaseSensitiveDuplicateDetection 验证切换 removeSlash 与 caseSensitive 时的冲突检测及状态回滚。
func TestSetRemoveSlashAndCaseSensitiveDuplicateDetection(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/docs", "Docs@Plain"); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/docs/", "Docs@Directory"); err != nil {
		t.Fatal(err)
	}
	if err := router.SetRemoveSlash(true); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("存在 /docs 与 /docs/ 时启用 removeSlash 应返回 ErrDuplicateRoute，实际=%v", err)
	}
	// 冲突失败后仍应维持严格斜杠模式，不可静默污染开关状态。
	matched, _, err := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/docs/", nil)))
	if err != nil || matched == nil || matched.Handler() != "Docs@Directory" {
		t.Fatalf("SetRemoveSlash 失败后应保持原模式: matched=%#v err=%v", matched, err)
	}

	// 开启 removeSlash=true 后，新注册同名不同尾斜杠的路由应直接冲突；唯一尾斜杠路由双向均可命中。
	relaxed := NewRouter()
	if err := relaxed.SetDefaultExtension(""); err != nil {
		t.Fatal(err)
	}
	if err := relaxed.SetRemoveSlash(true); err != nil {
		t.Fatal(err)
	}
	if _, err := relaxed.Get("/guide/", "Guide@Show"); err != nil {
		t.Fatal(err)
	}
	if _, err := relaxed.Get("/guide", "Guide@Conflict"); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("removeSlash=true 下 /guide 与 /guide/ 应冲突，实际=%v", err)
	}
	for _, path := range []string{"/guide", "/guide/"} {
		hit, _, matchErr := relaxed.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)))
		if matchErr != nil || hit == nil || hit.Handler() != "Guide@Show" {
			t.Fatalf("removeSlash=true 下 %q 应命中 Guide@Show: hit=%#v err=%v", path, hit, matchErr)
		}
	}

	// caseSensitive 从 true 切换为 false 时同样按 removeSlash 语义判定冲突。
	caseRouter := NewRouter()
	if err := caseRouter.SetCaseSensitive(true); err != nil {
		t.Fatal(err)
	}
	if _, err := caseRouter.Get("/Docs", "Docs@Upper"); err != nil {
		t.Fatal(err)
	}
	if _, err := caseRouter.Get("/docs/", "Docs@LowerSlash"); err != nil {
		t.Fatal(err)
	}
	if err := caseRouter.SetCaseSensitive(false); err != nil {
		t.Fatalf("removeSlash=false 下 /Docs 与 /docs/ 不应因大小写折叠误判冲突: %v", err)
	}
	caseConflict := NewRouter()
	if err := caseConflict.SetCaseSensitive(true); err != nil {
		t.Fatal(err)
	}
	if _, err := caseConflict.Get("/Docs/", "Docs@UpperSlash"); err != nil {
		t.Fatal(err)
	}
	if _, err := caseConflict.Get("/docs/", "Docs@LowerSlash"); err != nil {
		t.Fatal(err)
	}
	if err := caseConflict.SetCaseSensitive(false); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("大小写折叠后 /Docs/ 与 /docs/ 必须返回 ErrDuplicateRoute，实际=%v", err)
	}
}

// TestTrailingSlashRejectsDoubleSlashAndBlocksAutoRouteBypass 验证双斜杠非法路径被拒绝且自动路由不能绕过显式尾斜杠路由。
func TestTrailingSlashRejectsDoubleSlashAndBlocksAutoRouteBypass(t *testing.T) {
	router := NewRouter()
	for _, invalid := range []string{"//", "/docs//", "/api//docs/"} {
		if _, err := router.Get(invalid, "Docs@Invalid"); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("非法双斜杠路径 %q 应返回 ErrInvalidRoute，实际=%v", invalid, err)
		}
	}
	if err := router.Group("/api", func(g *Group) error {
		if _, err := g.Get("/docs//", "Docs@Invalid"); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("分组内双斜杠路径应返回 ErrInvalidRoute，实际=%v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := router.Group("/api//", func(g *Group) error { return nil }); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("含双斜杠的分组前缀应返回 ErrInvalidRoute，实际=%v", err)
	}

	autoRouter := NewRouter()
	if err := autoRouter.EnableAutoRoute(true); err != nil {
		t.Fatal(err)
	}
	if _, err := autoRouter.Get("/admin/", "Admin@Index"); err != nil {
		t.Fatal(err)
	}
	// 显式声明 /admin/ 后，请求 /admin 不得绕过显式路由落入自动调度。
	hit, _, err := autoRouter.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/admin", nil)))
	if err != nil || hit != nil {
		t.Fatalf("显式 /admin/ 存在时，/admin 不得回退到自动路由: hit=%#v err=%v", hit, err)
	}
}

// TestTrailingSlashURLAndNameCacheRoundTrip 验证命名路由 URL 生成及命名缓存导入导出完整保留尾斜杠并可回环匹配。
func TestTrailingSlashURLAndNameCacheRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name             string
		defaultExtension string
		removeSlash      bool
		wantDocsURL      string
		wantUserURL      string
	}{
		{
			name:             "严格斜杠且无默认后缀",
			defaultExtension: "",
			removeSlash:      false,
			wantDocsURL:      "/docs/",
			wantUserURL:      "/users/42/",
		},
		{
			name:             "严格斜杠且有默认后缀",
			defaultExtension: "html",
			removeSlash:      false,
			wantDocsURL:      "/docs.html/",
			wantUserURL:      "/users/42.html/",
		},
		{
			name:             "忽略斜杠且无默认后缀",
			defaultExtension: "",
			removeSlash:      true,
			wantDocsURL:      "/docs",
			wantUserURL:      "/users/42",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			if err := router.SetDefaultExtension(tc.defaultExtension); err != nil {
				t.Fatal(err)
			}
			if err := router.SetRemoveSlash(tc.removeSlash); err != nil {
				t.Fatal(err)
			}
			docsRoute, err := router.Get("/docs/", "Docs@Index")
			if err != nil {
				t.Fatal(err)
			}
			if err := docsRoute.WithName("docs.index"); err != nil {
				t.Fatal(err)
			}
			userRoute, err := router.Get("/users/:id/", "User@Show")
			if err != nil {
				t.Fatal(err)
			}
			if err := userRoute.WithName("users.show"); err != nil {
				t.Fatal(err)
			}

			docsURL, err := router.URL("docs.index", nil)
			if err != nil || docsURL != tc.wantDocsURL {
				t.Fatalf("docs.index URL 错误: got=%q want=%q err=%v", docsURL, tc.wantDocsURL, err)
			}
			userURL, err := router.URL("users.show", map[string]interface{}{"id": 42})
			if err != nil || userURL != tc.wantUserURL {
				t.Fatalf("users.show URL 错误: got=%q want=%q err=%v", userURL, tc.wantUserURL, err)
			}

			// 生成的 URL 必须回环命中原始路由。
			if hit, _, matchErr := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+docsURL, nil))); matchErr != nil || hit != docsRoute {
				t.Fatalf("生成的 URL %q 未回环命中 docsRoute: hit=%#v err=%v", docsURL, hit, matchErr)
			}
			if hit, params, matchErr := router.Match(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+userURL, nil))); matchErr != nil || hit != userRoute || params["id"] != "42" {
				t.Fatalf("生成的 URL %q 未回环命中 userRoute: hit=%#v params=%#v err=%v", userURL, hit, params, matchErr)
			}

			// 导出并重新加载命名路由缓存后，URL 生成结果保持完全一致。
			exported, err := router.ExportNameCache()
			if err != nil {
				t.Fatalf("导出命名路由缓存失败: %v", err)
			}
			restored := NewRouter()
			if err := restored.SetRemoveSlash(tc.removeSlash); err != nil {
				t.Fatal(err)
			}
			if err := restored.LoadNameCache(exported); err != nil {
				t.Fatalf("加载命名路由缓存失败: %v", err)
			}
			if cachedDocsURL, err := restored.URL("docs.index", nil); err != nil || cachedDocsURL != tc.wantDocsURL {
				t.Fatalf("缓存恢复后的 docs.index URL 错误: got=%q want=%q err=%v", cachedDocsURL, tc.wantDocsURL, err)
			}
			if cachedUserURL, err := restored.URL("users.show", map[string]interface{}{"id": 42}); err != nil || cachedUserURL != tc.wantUserURL {
				t.Fatalf("缓存恢复后的 users.show URL 错误: got=%q want=%q err=%v", cachedUserURL, tc.wantUserURL, err)
			}
		})
	}
}
