package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/ratelimit"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

type resettableCommitHookWriter struct {
	*httptest.ResponseRecorder
	hooks []func(http.Header) error
}

func (w *resettableCommitHookWriter) BeforeCommit(hook func(http.Header) error) error {
	w.hooks = append(w.hooks, hook)
	return nil
}

func (w *resettableCommitHookWriter) ResetUncommitted() bool {
	for key := range w.Header() {
		w.Header().Del(key)
	}
	w.hooks = nil
	return true
}

func (w *resettableCommitHookWriter) runHooks() error {
	var result error
	for _, h := range w.hooks {
		result = errors.Join(result, h(w.Header()))
	}
	w.hooks = nil
	return result
}

// TestIssue577SessionAbortsDirtyMutationsOnUnrecoveredPanic 验证内层发生未恢复 panic 时 Session 协调器放弃半截脏修改。
func TestIssue577SessionAbortsDirtyMutationsOnUnrecoveredPanic(t *testing.T) {
	backend := driver.NewMemory()
	manager := newMiddlewareSessionManager(t, backend)
	sessionMw := &Session{Manager: manager}
	recoveryMw := &Recovery{}

	// 即使底层 writer 不清空钩子，Session 自身的栈展开守卫也必须拒绝提交崩溃前的脏状态。
	plainHookWriter := &middlewareCommitHookWriter{}
	crashRaw := httptest.NewRequest(http.MethodGet, "http://example.com/crash", nil)
	crashReq := fwcontext.MustNewRequest(crashRaw, fwcontext.WithResponseWriter(plainHookWriter))

	resp := recoveryMw.Handle(crashReq, func(r *fwcontext.Request) *fwcontext.Response {
		return sessionMw.Handle(r, func(inner *fwcontext.Request) *fwcontext.Response {
			if err := inner.SetSession("elevated_admin", true); err != nil {
				t.Fatalf("写入请求内 Session 失败: %v", err)
			}
			panic("database rollback failure")
		})
	})
	if resp == nil || resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("Recovery 应返回 500 响应，实际: %#v", resp)
	}
	if err := plainHookWriter.commit(); err != nil {
		t.Fatalf("已中止的 Session 提交钩子不应报错: %v", err)
	}
	if len(plainHookWriter.Header().Values("Set-Cookie")) != 0 || len(resp.Headers().Values("Set-Cookie")) != 0 {
		t.Fatalf("崩溃请求不得下发 Session Set-Cookie: writer=%#v resp=%#v", plainHookWriter.Header(), resp.Headers())
	}

	// 支持 ResetUncommitted 的写入器同样保持干净。
	resetWriter := &resettableCommitHookWriter{ResponseRecorder: httptest.NewRecorder()}
	req2 := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/crash2", nil), fwcontext.WithResponseWriter(resetWriter))
	resp2 := recoveryMw.Handle(req2, func(r *fwcontext.Request) *fwcontext.Response {
		return sessionMw.Handle(r, func(inner *fwcontext.Request) *fwcontext.Response {
			_ = inner.SetSession("elevated_admin", true)
			panic("unexpected crash")
		})
	})
	_ = resetWriter.runHooks()
	if err := resp2.Send(resetWriter); err != nil {
		t.Fatal(err)
	}
	if resetWriter.Code != http.StatusInternalServerError || len(resetWriter.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("ResetUncommitted 后 500 响应不得携带 Set-Cookie: status=%d headers=%#v", resetWriter.Code, resetWriter.Header())
	}
}

// TestIssue579RateLimitAcceptsIPv6ZoneRemoteAddr 验证限流默认客户端 IP 键支持含网卡 Zone ID 的链路本地 IPv6。
func TestIssue579RateLimitAcceptsIPv6ZoneRemoteAddr(t *testing.T) {
	store, err := ratelimit.NewMemoryStore(64)
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := NewRateLimit(RateLimitConfig{
		Store: store,
		Limit: ratelimit.Limit{Rate: 5, Period: time.Minute, Burst: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/api", nil)
	raw.RemoteAddr = "[fe80::1%eth0]:54321"
	req := fwcontext.MustNewRequest(raw)
	resp := limiter.Handle(req, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	})
	if resp.GetStatus() != http.StatusOK {
		t.Fatalf("含 Zone ID 的 IPv6 直连请求限流应放行 200，实际为 %d", resp.GetStatus())
	}
}

// TestIssue581CSRFRejectsDuplicateAndEmptyOriginOrReferer 验证重复或空值 Origin/Referer 请求头无法绕过跨源校验。
func TestIssue581CSRFRejectsDuplicateAndEmptyOriginOrReferer(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("k", 32)
	csrfMw := newCSRFHandler(t, config)
	csrfCookie := issueCSRFTokenCookie(t, csrfMw)

	cases := []struct {
		name     string
		origins  []string
		referers []string
	}{
		{
			name:    "首行空 Origin 次行跨站 Origin",
			origins: []string{"", "https://evil.attacker"},
		},
		{
			name:    "首行同源 Origin 次行跨站 Origin",
			origins: []string{"https://example.com", "https://evil.attacker"},
		},
		{
			name:    "重复同源 Origin 头行",
			origins: []string{"https://example.com", "https://example.com"},
		},
		{
			name:    "单行显式空 Origin",
			origins: []string{""},
		},
		{
			name:    "单行空白分隔多源 Origin",
			origins: []string{"https://example.com https://evil.attacker"},
		},
		{
			name:     "空 Origin 加同源 Referer 也不得回退放行",
			origins:  []string{""},
			referers: []string{"https://example.com/form"},
		},
		{
			name:     "首行空 Referer 次行跨站 Referer",
			referers: []string{"", "https://evil.attacker/form"},
		},
		{
			name:     "首行同源 Referer 次行跨站 Referer",
			referers: []string{"https://example.com/form", "https://evil.attacker/form"},
		},
		{
			name:     "单行显式空 Referer",
			referers: []string{""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			postRaw := httptest.NewRequest(http.MethodPost, "https://example.com/form", nil)
			for _, o := range tc.origins {
				postRaw.Header.Add("Origin", o)
			}
			for _, r := range tc.referers {
				postRaw.Header.Add("Referer", r)
			}
			postRaw.AddCookie(csrfCookie)
			postRaw.Header.Set(config.HeaderName, csrfCookie.Value)
			postReq := fwcontext.MustNewRequest(postRaw)
			called := false
			resp := csrfMw(postReq, func(*fwcontext.Request) *fwcontext.Response {
				called = true
				return fwcontext.NewResponse().Content("mutated!")
			})
			if called || resp.GetStatus() != http.StatusForbidden {
				t.Fatalf("应拒绝异常 Origin/Referer 并返回 403: called=%t status=%d", called, resp.GetStatus())
			}
		})
	}
}

// TestIssue582CorsSafelistedHeadersIPv6CompressionAndStrictConfig 验证 Fetch 安全预检头、RFC 9110 空列表项、IPv6 压缩及畸形 AllowOrigins 拦截。
func TestIssue582CorsSafelistedHeadersIPv6CompressionAndStrictConfig(t *testing.T) {
	t.Run("default and custom CORS allow Fetch safelisted preflight headers and empty list items", func(t *testing.T) {
		for _, handler := range []Handler{
			Cors(),
			CorsWithConfig(CorsConfig{
				AllowOrigins: []string{"https://client.example"},
				AllowMethods: []string{http.MethodPost},
				AllowHeaders: []string{"Content-Type"},
			}),
		} {
			preRaw := httptest.NewRequest(http.MethodOptions, "https://example.com/api", nil)
			preRaw.Header.Set("Origin", "https://client.example")
			preRaw.Header.Set("Access-Control-Request-Method", http.MethodPost)
			preRaw.Header.Add("Access-Control-Request-Headers", "Content-Type, , Accept-Language, ")
			preRaw.Header.Add("Access-Control-Request-Headers", "Accept, Content-Language")
			preReq := fwcontext.MustNewRequest(preRaw)
			resp := handler(preReq, func(*fwcontext.Request) *fwcontext.Response {
				t.Fatal("预检请求不应进入下游")
				return nil
			})
			if resp.GetStatus() != http.StatusNoContent {
				t.Fatalf("携带 Fetch 安全头的预检请求应返回 204，实际 status=%d headers=%#v", resp.GetStatus(), resp.Headers())
			}
			allowedHeaders := strings.ToLower(resp.GetHeader("Access-Control-Allow-Headers"))
			for _, expected := range []string{"content-type", "accept", "accept-language", "content-language"} {
				if !strings.Contains(allowedHeaders, expected) {
					t.Fatalf("Access-Control-Allow-Headers 应包含 %q，实际为 %q", expected, resp.GetHeader("Access-Control-Allow-Headers"))
				}
			}
		}
	})

	t.Run("uncompressed IPv6 AllowOrigins matches RFC 5952 compressed browser Origin", func(t *testing.T) {
		handler, err := NewCors(CorsConfig{
			AllowOrigins: []string{
				"http://[2001:db8:0:0:0:0:0:1]:8080",
				"https://[2001:DB8:0:0:0:0:0:2]:443",
			},
			AllowMethods:     []string{http.MethodPost},
			AllowCredentials: true,
		})
		if err != nil {
			t.Fatalf("创建未压缩 IPv6 CORS 中间件失败: %v", err)
		}
		for _, origin := range []string{
			"http://[2001:db8::1]:8080",
			"https://[2001:db8::2]",
		} {
			pre := newCorsRequest(t, http.MethodOptions, origin)
			pre.Raw().Header.Set("Access-Control-Request-Method", http.MethodPost)
			resp := handler(pre, func(*fwcontext.Request) *fwcontext.Response {
				t.Fatal("预检不应执行下游")
				return nil
			})
			if resp.GetStatus() != http.StatusNoContent || resp.GetHeader("Access-Control-Allow-Origin") != origin {
				t.Fatalf("IPv6 压缩来源 %q 预检匹配失败: status=%d headers=%#v", origin, resp.GetStatus(), resp.Headers())
			}
			actual := handler(newCorsRequest(t, http.MethodPost, origin), func(*fwcontext.Request) *fwcontext.Response {
				return fwcontext.NewResponse().Content("ok")
			})
			if actual.GetHeader("Access-Control-Allow-Origin") != origin {
				t.Fatalf("IPv6 压缩来源 %q 普通请求匹配失败: headers=%#v", origin, actual.Headers())
			}
		}
	})

	t.Run("NewCors rejects ForceQuery fragment and uncanonicalizable AllowOrigins", func(t *testing.T) {
		invalidOrigins := []string{
			"http://example.com?",
			"https://example.com#",
			"ftp://example.com",
			"http://example.com:",
			"http://example.com:0",
			"http://[127.0.0.1]:8080",
			"http://[fe80::1%eth0]:8080",
			"http://[::gggg]:8080",
			"http://example%2ecom",
			"http:example.com",
		}
		for _, bad := range invalidOrigins {
			_, err := NewCors(CorsConfig{
				AllowOrigins: []string{bad},
				AllowMethods: []string{http.MethodGet},
			})
			if !errors.Is(err, ErrInvalidCorsConfig) {
				t.Fatalf("畸形 AllowOrigin %q 应返回 ErrInvalidCorsConfig，实际: %v", bad, err)
			}
		}
	})
}
