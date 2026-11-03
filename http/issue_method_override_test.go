package http

import (
	stdcontext "context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/auth"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/openapi"
)

func newIssueOpenAPIHandler(t *testing.T) http.Handler {
	t.Helper()
	registry, err := openapi.NewRegistry(openapi3.Info{Title: "dummy-private-schema", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := registry.Handler(stdcontext.Background())
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestIssue127StandardHandlerUsesAuthorizedMethod(t *testing.T) {
	for _, compression := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compression"}[compression], func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": compression, "min_size": 1})
			router := mustHTTPRoute(t, app)
			document := newIssueOpenAPIHandler(t)
			if _, err := router.Head("/api/spec", document); err != nil {
				t.Fatal(err)
			}
			getRoute, err := router.Get("/api/spec", document)
			if err != nil {
				t.Fatal(err)
			}
			if err := getRoute.WithMiddleware(auth.Require(nil, "spec.read")); err != nil {
				t.Fatal(err)
			}
			// 标准处理器即便不检查 HEAD，适配层仍必须抑制实体。
			if _, err := router.Head("/api/unconditional", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("dummy-private-schema"))
			})); err != nil {
				t.Fatal(err)
			}
			host := newTestHTTPHandler(t, app)
			for _, test := range []struct {
				name, method, path, override string
				status                      int
			}{
				{"head", http.MethodHead, "/api/spec", "", http.StatusOK},
				{"anonymous-get", http.MethodGet, "/api/spec", "", http.StatusUnauthorized},
				{"override-head", http.MethodGet, "/api/spec", http.MethodHead, http.StatusOK},
				{"unconditional-head", http.MethodGet, "/api/unconditional", http.MethodHead, http.StatusOK},
			} {
				t.Run(test.name, func(t *testing.T) {
					req := httptest.NewRequest(test.method, "http://127.0.0.1"+test.path, nil)
					req.Header.Set("Accept-Encoding", "gzip")
					if test.override != "" {
						req.Header.Set("X-HTTP-Method-Override", test.override)
					}
					recorder := httptest.NewRecorder()
					host.ServeHTTP(recorder, req)
					if recorder.Code != test.status {
						t.Fatalf("status=%d, want %d; body=%q", recorder.Code, test.status, recorder.Body.String())
					}
					if strings.Contains(recorder.Body.String(), "dummy-private-schema") || test.status == http.StatusOK && recorder.Body.Len() != 0 {
						t.Fatalf("HEAD/未授权响应泄漏文档: %q", recorder.Body.String())
					}
					if req.Method != test.method {
						t.Fatalf("原始方法被改写: %q", req.Method)
					}
				})
			}
		})
	}
}

func TestIssue127MethodAdapterPreservesContextAndOrigin(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	ctx, cancel := stdcontext.WithCancel(stdcontext.WithValue(stdcontext.Background(), httpTestContextKey{}, "preserved"))
	defer cancel()
	matched, err := mustHTTPRoute(t, app).Patch("/method", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPatch || req.Context().Value(httpTestContextKey{}) != "preserved" {
			t.Errorf("有效方法或上下文丢失: method=%q", req.Method)
		}
		cancel()
		if req.Context().Err() != stdcontext.Canceled {
			t.Error("取消信号没有传递给标准处理器")
		}
		req.Header.Set("X-Handler-Only", "changed")
		w.WriteHeader(http.StatusAccepted)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := matched.WithMiddleware(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		resp := next(req)
		if req.Method(true) != http.MethodGet {
			t.Errorf("审计原始方法被改写: %s", req.Method(true))
		}
		return resp
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/method", nil).WithContext(ctx)
	req.Header.Set("X-HTTP-Method-Override", http.MethodPatch)
	recorder := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusAccepted || req.Method != http.MethodGet || req.Header.Get("X-Handler-Only") != "" {
		t.Fatalf("请求快照/响应错误: status=%d method=%q headers=%v", recorder.Code, req.Method, req.Header)
	}
}
