package http

import (
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	sessionDriver "github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

// TestIssue575CookieServiceCoexistsWithCSRFAndResponseCookie 验证控制器通过 cookie.Cookie.Set 写入的业务 Cookie 不会被 CSRF 或 Response.Cookie 清空。
func TestIssue575CookieServiceCoexistsWithCSRFAndResponseCookie(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(fmt.Sprintf("compress=%t", compress), func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": compress, "min_size": 1})
			csrfCfg := middleware.DefaultCSRFConfig()
			csrfCfg.Secret = strings.Repeat("x", 32)
			csrfMw, err := middleware.CsrfWithConfig(csrfCfg)
			if err != nil {
				t.Fatal(err)
			}
			mustHTTPMiddleware(t, app).Pipe(csrfMw)
			if _, err := mustHTTPRoute(t, app).Get("/login-landing", func(req *fwcontext.Request) *fwcontext.Response {
				writer, ok := req.ResponseWriter()
				if !ok {
					t.Fatal("缺少底层 ResponseWriter")
				}
				cSvc, err := cookie.NewCookieForRequest(cookie.DefaultConfig(), req.Raw(), writer)
				if err != nil {
					t.Fatal(err)
				}
				if err := cSvc.Set("auth_token", "secret123"); err != nil {
					t.Fatal(err)
				}
				return fwcontext.NewResponse().Cookie("theme", "dark").Content("welcome")
			}); err != nil {
				t.Fatal(err)
			}

			kernel := newTestHTTPHandler(t, app)
			raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/login-landing", nil)
			raw.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			kernel.ServeHTTP(rec, raw)

			if rec.Code != stdhttp.StatusOK {
				t.Fatalf("预期 200，实际 %d", rec.Code)
			}
			found := make(map[string]string)
			counts := make(map[string]int)
			for _, item := range rec.Result().Cookies() {
				found[item.Name] = item.Value
				counts[item.Name]++
			}
			if found["auth_token"] != "secret123" || found["theme"] != "dark" || found[csrfCfg.CookieName] == "" {
				t.Fatalf("业务 Cookie、Response.Cookie 与 CSRF Cookie 必须同时保留: %#v", rec.Header().Values("Set-Cookie"))
			}
			if counts["auth_token"] != 1 || counts["theme"] != 1 || counts[csrfCfg.CookieName] != 1 {
				t.Fatalf("Set-Cookie 不得重复发送: %#v", rec.Header().Values("Set-Cookie"))
			}
		})
	}
}

// TestIssue577RecoveryWithSessionDropsDirtyPanicMutations 验证 Recovery 捕获下游 panic 返回 500 时不会持久化崩溃前的脏 Session 状态或下发新 Set-Cookie。
func TestIssue577RecoveryWithSessionDropsDirtyPanicMutations(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(fmt.Sprintf("compress=%t", compress), func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": compress, "min_size": 1024})
			backend := sessionDriver.NewMemory()
			sessMgr := newHTTPCommitSessionManager(t, backend)

			mustHTTPMiddleware(t, app).Pipe((&middleware.Recovery{}).Handle)
			mustHTTPMiddleware(t, app).Pipe((&middleware.Session{Manager: sessMgr}).Handle)

			router := mustHTTPRoute(t, app)
			if _, err := router.Get("/seed", func(req *fwcontext.Request) *fwcontext.Response {
				if err := req.SetSession("role", "member"); err != nil {
					t.Fatal(err)
				}
				return fwcontext.NewResponse().Content("seeded")
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.Get("/crash", func(req *fwcontext.Request) *fwcontext.Response {
				_ = req.SetSession("elevated_admin", true)
				_ = req.SetSession("role", "superadmin")
				panic("database rollback failure")
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.Get("/inspect", func(req *fwcontext.Request) *fwcontext.Response {
				return fwcontext.NewResponse().Json(map[string]interface{}{
					"role":  req.Session("role"),
					"admin": req.Session("elevated_admin") != nil,
				})
			}); err != nil {
				t.Fatal(err)
			}

			kernel := newTestHTTPHandler(t, app)

			// 1. 新会话请求直接崩溃：不落盘、不返回 Set-Cookie。
			freshCrash := httptest.NewRecorder()
			rawCrash := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/crash", nil)
			rawCrash.Header.Set("Accept-Encoding", "gzip")
			kernel.ServeHTTP(freshCrash, rawCrash)
			if freshCrash.Code != stdhttp.StatusInternalServerError || len(freshCrash.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("崩溃请求应返回干净 500 且无 Set-Cookie: status=%d cookies=%#v", freshCrash.Code, freshCrash.Header().Values("Set-Cookie"))
			}

			// 2. 已有正常会话下崩溃：原有 role=member 保持不变，半截脏修改未持久化。
			seedRec := httptest.NewRecorder()
			kernel.ServeHTTP(seedRec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/seed", nil))
			seedCookies := seedRec.Result().Cookies()
			if len(seedCookies) != 1 {
				t.Fatalf("正常请求应下发 Session Cookie: %#v", seedCookies)
			}
			sessCookie := seedCookies[0]

			crashWithSession := httptest.NewRecorder()
			crashReq2 := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/crash", nil)
			crashReq2.AddCookie(sessCookie)
			crashReq2.Header.Set("Accept-Encoding", "gzip")
			kernel.ServeHTTP(crashWithSession, crashReq2)
			if crashWithSession.Code != stdhttp.StatusInternalServerError || len(crashWithSession.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("已有会话请求崩溃不应下发新 Set-Cookie: status=%d cookies=%#v", crashWithSession.Code, crashWithSession.Header().Values("Set-Cookie"))
			}

			inspectRec := httptest.NewRecorder()
			inspectReq := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/inspect", nil)
			inspectReq.AddCookie(sessCookie)
			kernel.ServeHTTP(inspectRec, inspectReq)
			if inspectRec.Code != stdhttp.StatusOK || !strings.Contains(inspectRec.Body.String(), `"role":"member"`) || !strings.Contains(inspectRec.Body.String(), `"admin":false`) {
				t.Fatalf("崩溃前的半截脏 Session 不得覆盖已有持久化会话: status=%d body=%s", inspectRec.Code, inspectRec.Body.String())
			}
		})
	}
}

// TestIssue578ParseErrorPreservesGlobalMiddlewareHeadersAndCompressionStatus 验证解析错误保留全局中间件头且压缩缓冲期 Status 准确。
func TestIssue578ParseErrorPreservesGlobalMiddlewareHeadersAndCompressionStatus(t *testing.T) {
	t.Run("malformed JSON and late ParseError preserve global CORS and trace headers", func(t *testing.T) {
		app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": true, "min_size": 1024})
		mustHTTPConfig(t, app).Set("app.server.max_body_bytes", 32)
		corsMw, err := middleware.NewCors(middleware.CorsConfig{
			AllowOrigins: []string{"https://trusted.example"},
			AllowMethods: []string{stdhttp.MethodPost},
		})
		if err != nil {
			t.Fatal(err)
		}
		mustHTTPMiddleware(t, app).Pipe(corsMw)
		mustHTTPMiddleware(t, app).Pipe(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			res := next(req)
			res.Header("X-Request-Id", "req-578")
			return res
		})

		router := mustHTTPRoute(t, app)
		if _, err := router.Post("/api", func(*fwcontext.Request) *fwcontext.Response {
			return fwcontext.NewResponse().Content("ok")
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := router.Post("/late-parse", func(req *fwcontext.Request) *fwcontext.Response {
			req.WithInput("{broken")
			_ = req.Post("field")
			return fwcontext.NewResponse().Content("ok")
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := router.Post("/buffered-stdlib", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			w.WriteHeader(stdhttp.StatusAccepted)
			_, _ = w.Write([]byte("partial-ok"))
			buf := make([]byte, 64)
			_, _ = r.Body.Read(buf)
		})); err != nil {
			t.Fatal(err)
		}

		kernel := newTestHTTPHandler(t, app)

		// 1. 入口畸形 JSON -> 400 且保留 ACAO、Vary、X-Request-Id
		badJSONReq := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/api", strings.NewReader("{invalid"))
		badJSONReq.Header.Set("Content-Type", "application/json")
		badJSONReq.Header.Set("Origin", "https://trusted.example")
		badJSONRec := httptest.NewRecorder()
		kernel.ServeHTTP(badJSONRec, badJSONReq)
		if badJSONRec.Code != stdhttp.StatusBadRequest ||
			badJSONRec.Header().Get("Access-Control-Allow-Origin") != "https://trusted.example" ||
			badJSONRec.Header().Get("X-Request-Id") != "req-578" {
			t.Fatalf("畸形 JSON 400 响应丢失全局中间件头: status=%d headers=%#v", badJSONRec.Code, badJSONRec.Header())
		}

		// 2. 超限请求体 -> 413 且保留 ACAO、X-Request-Id
		largeReq := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/api", strings.NewReader(`{"data":"`+strings.Repeat("a", 64)+`"}`))
		largeReq.Header.Set("Content-Type", "application/json")
		largeReq.Header.Set("Origin", "https://trusted.example")
		largeRec := httptest.NewRecorder()
		kernel.ServeHTTP(largeRec, largeReq)
		if largeRec.Code != stdhttp.StatusRequestEntityTooLarge ||
			largeRec.Header().Get("Access-Control-Allow-Origin") != "https://trusted.example" ||
			largeRec.Header().Get("X-Request-Id") != "req-578" {
			t.Fatalf("超限请求体 413 响应丢失全局中间件头: status=%d headers=%#v", largeRec.Code, largeRec.Header())
		}

		// 3. 路由内后发 ParseError -> 400 且保留全局中间件后置头
		lateReq := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/late-parse", nil)
		lateReq.Header.Set("Content-Type", "application/json")
		lateReq.Header.Set("Origin", "https://trusted.example")
		lateRec := httptest.NewRecorder()
		kernel.ServeHTTP(lateRec, lateReq)
		if lateRec.Code != stdhttp.StatusBadRequest ||
			lateRec.Header().Get("Access-Control-Allow-Origin") != "https://trusted.example" ||
			lateRec.Header().Get("X-Request-Id") != "req-578" {
			t.Fatalf("后发 ParseError 400 响应丢失全局中间件头: status=%d headers=%#v", lateRec.Code, lateRec.Header())
		}

		// 4. 标准库 Handler 已向压缩写入器写入缓冲内容时，不得拼接第二份错误响应体
		bufStdReq := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/buffered-stdlib", nil)
		bufStdReq.Header.Set("Accept-Encoding", "gzip")
		bufStdReq.Header.Set("Origin", "https://trusted.example")
		bufStdRec := httptest.NewRecorder()
		kernel.ServeHTTP(bufStdRec, bufStdReq)
		if bufStdRec.Code != stdhttp.StatusAccepted || bufStdRec.Body.String() != "partial-ok" {
			t.Fatalf("标准库 Handler 缓冲状态或正文被污染: status=%d body=%q", bufStdRec.Code, bufStdRec.Body.String())
		}
	})

	t.Run("CompressionResponseWriter.Status reflects buffered rejected and committed status", func(t *testing.T) {
		rec := httptest.NewRecorder()
		reqWithGzip := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
		reqWithGzip.Header.Set("Accept-Encoding", "gzip")
		cw := NewCompressionResponseWriter(rec, reqWithGzip, 1024, nil)
		if cw.Status() != stdhttp.StatusOK {
			t.Fatalf("初始默认状态应为 200，实际 %d", cw.Status())
		}
		cw.WriteHeader(stdhttp.StatusForbidden)
		if cw.Status() != stdhttp.StatusForbidden {
			t.Fatalf("WriteHeader(403) 后缓冲期 Status() 应立即返回 403，实际 %d", cw.Status())
		}
		_, _ = cw.Write([]byte("short forbidden"))
		if cw.Status() != stdhttp.StatusForbidden {
			t.Fatalf("缓冲写入后 Close 前 Status() 应保持 403，实际 %d", cw.Status())
		}
		if err := cw.Close(); err != nil {
			t.Fatal(err)
		}
		if cw.Status() != stdhttp.StatusForbidden || rec.Code != stdhttp.StatusForbidden {
			t.Fatalf("Close 后 Status() 与底层状态均应为 403: cw=%d rec=%d", cw.Status(), rec.Code)
		}

		// 406 编码拒绝路径
		rejectRec := httptest.NewRecorder()
		rejectReq := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
		rejectReq.Header.Set("Accept-Encoding", "identity;q=0")
		rejectCW := NewCompressionResponseWriter(rejectRec, rejectReq, 1024, nil)
		rejectCW.Header().Set("Content-Type", "image/png")
		rejectCW.WriteHeader(stdhttp.StatusOK)
		_, _ = rejectCW.Write([]byte("png-bytes"))
		if err := rejectCW.Close(); err != nil {
			t.Fatal(err)
		}
		if rejectCW.Status() != stdhttp.StatusNotAcceptable || rejectRec.Code != stdhttp.StatusNotAcceptable {
			t.Fatalf("406 拒绝编码后 Status() 应返回 406: cw=%d rec=%d", rejectCW.Status(), rejectRec.Code)
		}
	})
}

type issue579PriceController struct{}

func (*issue579PriceController) Quote(price float64) *fwcontext.Response {
	return fwcontext.NewResponse().Json(map[string]float64{"price": price})
}

// TestIssue579ScalarFloatBindingRejectsNaNAndInf 验证闭包与控制器方法标量浮点绑定拒绝 NaN/±Inf 并返回 400 而非 500。
func TestIssue579ScalarFloatBindingRejectsNaNAndInf(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := app.BindFactory("Price", func() interface{} { return &issue579PriceController{} }); err != nil {
		t.Fatal(err)
	}
	router := mustHTTPRoute(t, app)
	if err := router.SetDefaultPattern(`[^/]+`); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/price/:v", func(v float64) *fwcontext.Response {
		return fwcontext.NewResponse().Json(map[string]float64{"v": v})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/price32/:v", func(v float32) *fwcontext.Response {
		return fwcontext.NewResponse().Json(map[string]float32{"v": v})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/quote/:price", "price/quote"); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Get("/query-price", func(v float64) *fwcontext.Response {
		return fwcontext.NewResponse().Json(map[string]float64{"v": v})
	}); err != nil {
		t.Fatal(err)
	}

	kernel := newTestHTTPHandler(t, app)

	for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "+Infinity", "-Infinity"} {
		for _, prefix := range []string{"/price/", "/price32/", "/quote/"} {
			rec := httptest.NewRecorder()
			kernel.ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com"+prefix+bad, nil))
			if rec.Code != stdhttp.StatusBadRequest {
				t.Fatalf("GET %s%s 应返回 400 Bad Request，实际返回 %d: %s", prefix, bad, rec.Code, rec.Body.String())
			}
		}
	}
	for _, badQuery := range []string{"NaN", "Inf", "%2BInf", "-Inf", "%2BInfinity"} {
		rec := httptest.NewRecorder()
		kernel.ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/query-price?v="+badQuery, nil))
		if rec.Code != stdhttp.StatusBadRequest {
			t.Fatalf("GET /query-price?v=%s 应返回 400 Bad Request，实际返回 %d: %s", badQuery, rec.Code, rec.Body.String())
		}
	}

	okRec := httptest.NewRecorder()
	kernel.ServeHTTP(okRec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/price/19.95", nil))
	if okRec.Code != stdhttp.StatusOK || strings.TrimSpace(okRec.Body.String()) != `{"v":19.95}` {
		t.Fatalf("合法有限浮点数应正常返回 200 JSON: status=%d body=%q", okRec.Code, okRec.Body.String())
	}
}
