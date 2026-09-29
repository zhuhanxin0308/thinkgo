package http

import (
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

func TestAuditCSRFCookieMustReachStandardHandlerClient(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		for _, mode := range []string{"standard", "write_header", "flush", "framework_buffered"} {
			t.Run(mode+"/compression="+strconv.FormatBool(compressed), func(t *testing.T) {
				app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": compressed})
				cfg := middleware.DefaultCSRFConfig()
				cfg.Secret = strings.Repeat("a", 32) // Test-only fixed secret.
				csrf, err := middleware.CsrfWithConfig(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var handler interface{}
				if mode == "framework_buffered" {
					handler = func(*fwcontext.Request) *fwcontext.Response {
						return fwcontext.NewResponse().Content("page")
					}
				} else {
					handler = stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						if mode == "write_header" {
							w.WriteHeader(stdhttp.StatusOK)
							return
						}
						if mode == "flush" {
							if err := stdhttp.NewResponseController(w).Flush(); err != nil {
								t.Error(err)
							}
						}
						if _, err := io.WriteString(w, strings.Repeat("page", 1024)); err != nil {
							t.Error(err)
						}
					})
				}
				router := mustHTTPRoute(t, app)
				if _, err := router.Get("/audit/csrf", handler, csrf); err != nil {
					t.Fatal(err)
				}
				if _, err := router.Post("/audit/csrf", func(*fwcontext.Request) *fwcontext.Response {
					return fwcontext.NewResponse().Content("accepted")
				}, csrf); err != nil {
					t.Fatal(err)
				}
				kernel, err := NewHttp(app)
				if err != nil {
					t.Fatal(err)
				}
				get := httptest.NewRequest(stdhttp.MethodGet, "http://example.test/audit/csrf", nil)
				get.Header.Set("Accept-Encoding", "gzip")
				recorder := httptest.NewRecorder()
				kernel.ServeHTTP(recorder, get)
				result := recorder.Result()
				defer result.Body.Close()
				if result.StatusCode != stdhttp.StatusOK {
					t.Fatalf("GET failed: status=%d body=%q", result.StatusCode, recorder.Body.String())
				}
				var token *stdhttp.Cookie
				for _, cookie := range result.Cookies() {
					if cookie.Name == cfg.CookieName {
						if token != nil {
							t.Fatal("CSRF cookie sent more than once")
						}
						token = cookie
					}
				}
				if token == nil || token.Value == "" {
					t.Fatalf("first GET has no CSRF cookie on wire: %v", result.Header)
				}
				for _, submitToken := range []bool{true, false} {
					post := httptest.NewRequest(stdhttp.MethodPost, "http://example.test/audit/csrf", nil)
					post.AddCookie(token)
					post.Header.Set("Origin", "http://example.test")
					want := stdhttp.StatusForbidden
					if submitToken {
						post.Header.Set(cfg.HeaderName, token.Value)
						want = stdhttp.StatusOK
					}
					written := httptest.NewRecorder()
					kernel.ServeHTTP(written, post)
					if written.Code != want {
						t.Fatalf("POST token=%t: got=%d want=%d body=%q", submitToken, written.Code, want, written.Body.String())
					}
				}
			})
		}
	}
}
