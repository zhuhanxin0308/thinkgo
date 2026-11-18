package context_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

// TestIssue575WriteHeadersPreservesExistingSetCookies 验证 Response.Send 不会清空底层 ResponseWriter 已写入的业务 Cookie。
func TestIssue575WriteHeadersPreservesExistingSetCookies(t *testing.T) {
	t.Run("cookie service coexists with Response.Cookie", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rawReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		cSvc, err := cookie.NewCookieForRequest(cookie.DefaultConfig(), rawReq, rec)
		if err != nil {
			t.Fatalf("创建请求 Cookie 服务失败: %v", err)
		}
		if err := cSvc.Set("auth_token", "secret123"); err != nil {
			t.Fatalf("写入 auth_token 失败: %v", err)
		}
		resp := fwcontext.NewResponse().Cookie("theme", "dark").Content("ok")
		if err := resp.Send(rec); err != nil {
			t.Fatalf("发送响应失败: %v", err)
		}
		cookies := rec.Result().Cookies()
		got := make(map[string]string, len(cookies))
		for _, item := range cookies {
			got[item.Name] = item.Value
		}
		if len(cookies) != 2 || got["auth_token"] != "secret123" || got["theme"] != "dark" {
			t.Fatalf("应同时保留 auth_token 与 theme 两条 Cookie，实际: %#v", rec.Header().Values("Set-Cookie"))
		}
	})

	t.Run("same name domain path overrides stale cookie while keeping others", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rawReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		cSvc, err := cookie.NewCookieForRequest(cookie.DefaultConfig(), rawReq, rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := cSvc.Set("auth_token", "secret123"); err != nil {
			t.Fatal(err)
		}
		if err := cSvc.Set("theme", "light"); err != nil {
			t.Fatal(err)
		}
		if err := cSvc.Set("theme", "admin-light", cookie.CookieOptions{Path: "/admin"}); err != nil {
			t.Fatal(err)
		}
		// 同时覆盖精确重复行与非规范大小写头键。
		exactLine := "csrf_token=fixed; Path=/; SameSite=Lax"
		rec.Header()["set-cookie"] = []string{exactLine}

		resp := fwcontext.NewResponse().
			Cookie("theme", "dark").
			AddHeader("Set-Cookie", exactLine).
			Content("ok")
		if err := resp.Send(rec); err != nil {
			t.Fatal(err)
		}
		cookies := rec.Result().Cookies()
		if len(cookies) != 4 {
			t.Fatalf("预期保留 4 条互不冲突 Cookie，实际 %d 条: %#v", len(cookies), rec.Header().Values("Set-Cookie"))
		}
		seenThemeRoot := ""
		seenThemeAdmin := ""
		csrfCount := 0
		authVal := ""
		for _, item := range cookies {
			switch {
			case item.Name == "auth_token":
				authVal = item.Value
			case item.Name == "theme" && item.Path == "/":
				seenThemeRoot = item.Value
			case item.Name == "theme" && item.Path == "/admin":
				seenThemeAdmin = item.Value
			case item.Name == "csrf_token":
				csrfCount++
			}
		}
		if authVal != "secret123" || seenThemeRoot != "dark" || seenThemeAdmin != "admin-light" || csrfCount != 1 {
			t.Fatalf("Cookie 合并结果不符合同名同域同路径语义: %#v", rec.Header().Values("Set-Cookie"))
		}
	})

	t.Run("internal error fallback still wipes uncommitted cookies", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rawReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		cSvc, _ := cookie.NewCookieForRequest(cookie.DefaultConfig(), rawReq, rec)
		_ = cSvc.Set("auth_token", "secret123")
		resp := fwcontext.NewResponse().Cookie("theme", "dark").Redirect("", http.StatusFound)
		if err := resp.Send(rec); !errors.Is(err, fwcontext.ErrInvalidRedirect) {
			t.Fatalf("非法重定向应返回 ErrInvalidRedirect，实际: %v", err)
		}
		if len(rec.Header().Values("Set-Cookie")) != 0 || rec.Code != http.StatusInternalServerError {
			t.Fatalf("500 降级路径必须清空未提交 Cookie: status=%d headers=%#v", rec.Code, rec.Header())
		}
	})
}

// TestIssue576RedirectClearsBoundEntitiesAndHeaders 验证 Redirect/RedirectLocal 清除已绑定的下载文件、流与实体正文。
func TestIssue576RedirectClearsBoundEntitiesAndHeaders(t *testing.T) {
	tempDir := t.TempDir()
	secretFile := filepath.Join(tempDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("TOP_SECRET_PDF_CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	missingFile := filepath.Join(tempDir, "missing.txt")

	buildCases := []struct {
		name  string
		build func(*bool) *fwcontext.Response
	}{
		{
			name: "Download existing file",
			build: func(_ *bool) *fwcontext.Response {
				return fwcontext.NewResponse().Download(secretFile, "secret.txt")
			},
		},
		{
			name: "Download missing file",
			build: func(_ *bool) *fwcontext.Response {
				return fwcontext.NewResponse().Download(missingFile, "missing.txt")
			},
		},
		{
			name: "DownloadSafe existing file",
			build: func(_ *bool) *fwcontext.Response {
				return fwcontext.NewResponse().DownloadSafe(tempDir, "secret.txt", "secret.txt")
			},
		},
		{
			name: "DownloadSafe missing file",
			build: func(_ *bool) *fwcontext.Response {
				return fwcontext.NewResponse().DownloadSafe(tempDir, "missing.txt", "missing.txt")
			},
		},
		{
			name: "Stream callback",
			build: func(streamCalled *bool) *fwcontext.Response {
				return fwcontext.NewResponse().Stream(func(w io.Writer) error {
					*streamCalled = true
					_, err := io.WriteString(w, "TOP_SECRET_STREAM")
					return err
				})
			},
		},
		{
			name: "Json entity",
			build: func(_ *bool) *fwcontext.Response {
				return fwcontext.NewResponse().Json(map[string]string{"secret": "TOP_SECRET_JSON"})
			},
		},
		{
			name: "Chunk entity",
			build: func(_ *bool) *fwcontext.Response {
				return fwcontext.NewResponse().Chunk([][]byte{[]byte("TOP_SECRET_CHUNK")})
			},
		},
	}

	for _, tc := range buildCases {
		for _, local := range []bool{false, true} {
			mode := "Redirect"
			if local {
				mode = "RedirectLocal"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				streamCalled := false
				resp := tc.build(&streamCalled)
				if local {
					resp.RedirectLocal("/login", http.StatusFound)
				} else {
					resp.Redirect("/login", http.StatusFound)
				}
				rec := httptest.NewRecorder()
				if err := resp.Send(rec); err != nil {
					t.Fatalf("发送重定向响应失败: %v", err)
				}
				if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
					t.Fatalf("重定向状态或 Location 错误: status=%d location=%q", rec.Code, rec.Header().Get("Location"))
				}
				if rec.Body.Len() != 0 || len(resp.GetBody()) != 0 || resp.GetData() != nil || streamCalled {
					t.Fatalf("重定向不得泄露实体内容或调用流回调: body=%q data=%#v streamCalled=%t", rec.Body.String(), resp.GetData(), streamCalled)
				}
				for _, forbidden := range []string{"Content-Disposition", "Content-Length", "Content-Range"} {
					if val := rec.Header().Get(forbidden); val != "" {
						t.Fatalf("重定向不得残留旧实体头 %s=%q", forbidden, val)
					}
				}
				if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "octet-stream") || strings.Contains(ct, "json") {
					t.Fatalf("重定向不得残留原下载或 JSON 的 Content-Type: %q", ct)
				}
			})
		}
	}
}

// TestIssue579BuildApplicationPathRejectsDotSegments 验证 BuildApplicationPath 拦截所有字面及编码形式的点路径段穿越。
func TestIssue579BuildApplicationPathRejectsDotSegments(t *testing.T) {
	rejected := []string{
		"../other/secret",
		"/../other/secret",
		"/admin/../other/secret",
		"/admin/..",
		"..",
		".",
		"./secret",
		"/admin/./secret",
		"/admin/.",
		"%2e/secret",
		"%2E/secret",
		"%2e%2e/secret",
		"%2e./other/secret",
		".%2e/other/secret",
		"%2E./other/secret",
		".%2E/other/secret",
		"/admin/%2e./other/secret",
		"a%2f..%2fsecret",
		"a%5c..%5csecret",
		`..\other\secret`,
	}
	for _, input := range rejected {
		if got, err := fwcontext.BuildApplicationPath("/admin", input); !errors.Is(err, fwcontext.ErrInvalidApplicationPath) {
			t.Fatalf("BuildApplicationPath(%q) 应返回 ErrInvalidApplicationPath，实际 got=%q err=%v", input, got, err)
		}
	}

	if got, err := fwcontext.BuildApplicationPath("/admin/../escape", "/dashboard"); !errors.Is(err, fwcontext.ErrInvalidApplicationPath) {
		t.Fatalf("非法应用前缀应被拒绝: got=%q err=%v", got, err)
	}

	valid := []struct {
		prefix string
		path   string
		want   string
	}{
		{prefix: "/admin", path: "reports/2026.01.pdf?download=1", want: "/admin/reports/2026.01.pdf?download=1"},
		{prefix: "/admin", path: "/admin/v1.2/users", want: "/admin/v1.2/users"},
		{prefix: "/admin", path: "/", want: "/admin/"},
	}
	for _, tc := range valid {
		got, err := fwcontext.BuildApplicationPath(tc.prefix, tc.path)
		if err != nil || got != tc.want {
			t.Fatalf("BuildApplicationPath(%q, %q) = (%q, %v), want (%q, nil)", tc.prefix, tc.path, got, err, tc.want)
		}
	}
}

// TestIssue579RemoteIPSupportsIPv6ZoneID 验证链路本地带网卡 Zone ID 的 IPv6 地址可正确解析并匹配可信代理网段。
func TestIssue579RemoteIPSupportsIPv6ZoneID(t *testing.T) {
	for _, remoteAddr := range []string{"[fe80::1%eth0]:54321", "fe80::1%eth0", "[fe80::1%en0]"} {
		raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		raw.RemoteAddr = remoteAddr
		req, err := fwcontext.NewRequest(raw)
		if err != nil {
			t.Fatalf("NewRequest(%q) 失败: %v", remoteAddr, err)
		}
		if req.Ip() != "fe80::1" || !req.IsValidIP(req.Ip()) || !req.IsValidIP(req.Ip(), "ipv6") {
			t.Fatalf("RemoteAddr=%q 解析错误: ip=%q valid=%t", remoteAddr, req.Ip(), req.IsValidIP(req.Ip()))
		}
		if got := req.Server("REMOTE_ADDR"); got != "fe80::1" {
			t.Fatalf("REMOTE_ADDR 应规范化为 fe80::1，实际为 %q", got)
		}
	}

	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.RemoteAddr = "[fe80::1%eth0]:54321"
	raw.Header.Set("X-Forwarded-For", "2001:db8::9")
	raw.Header.Set("X-Forwarded-Proto", "https")
	req, err := fwcontext.NewRequest(raw, fwcontext.WithTrustedProxies([]string{"fe80::/10"}))
	if err != nil {
		t.Fatal(err)
	}
	if req.Ip() != "2001:db8::9" || !req.IsSsl() {
		t.Fatalf("带 Zone ID 的链路本地反代未命中 fe80::/10: ip=%q ssl=%t", req.Ip(), req.IsSsl())
	}

	rawRealIP := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	rawRealIP.RemoteAddr = "[fe80::1%eth0]:54321"
	rawRealIP.Header.Set("X-Real-IP", "fe80::2%eth0")
	reqRealIP, err := fwcontext.NewRequest(rawRealIP, fwcontext.WithTrustedProxies([]string{"fe80::/10"}))
	if err != nil {
		t.Fatal(err)
	}
	if reqRealIP.Ip() != "fe80::2" || !reqRealIP.IsValidIP(reqRealIP.Ip(), "ipv6") {
		t.Fatalf("X-Real-IP 带 Zone ID 应规范化为 fe80::2，实际: %q", reqRealIP.Ip())
	}
}
