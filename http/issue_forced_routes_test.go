package http

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type issue17Controller struct{ calls *atomic.Int64 }

func (c *issue17Controller) Delete() string { c.calls.Add(1); return "deleted" }
func (c *issue17Controller) Hidden() string { c.calls.Add(1); return "hidden-action" }

// 验证实际控制器存在：404 必须来自路由策略，而不是控制器解析失败。
func TestIssue17HTTPDoesNotInvokeUnregisteredControllerActions(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
		var calls atomic.Int64
		if err := app.BindFactory("Danger", func() interface{} { return &issue17Controller{calls: &calls} }); err != nil {
			t.Fatal(err)
		}
		router := mustHTTPRoute(t, app)
		if optIn {
			if err := router.EnableAutoRoute(true); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := router.Post("/danger/delete", "Danger@Delete"); err != nil {
			t.Fatal(err)
		}
		handler := newTestHTTPHandler(t, app)
		if optIn {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.com/danger/hidden", nil))
			if response.Code != http.StatusOK || calls.Load() != 1 || response.Body.String() != "hidden-action" {
				t.Fatalf("explicit opt-in did not resolve real controller: %d %s calls=%d", response.Code, response.Body.String(), calls.Load())
			}
			continue
		}
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			for _, path := range []string{"/danger/delete", "/danger/hidden"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(method, "http://example.com"+path, nil))
				if response.Code != http.StatusNotFound || calls.Load() != 0 {
					t.Errorf("%s %s: status=%d calls=%d", method, path, response.Code, calls.Load())
				}
			}
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://example.com/danger/delete", nil))
		if response.Code != http.StatusOK || calls.Load() != 1 || response.Body.String() != "deleted" {
			t.Fatalf("registered POST did not execute exactly once: %d calls=%d", response.Code, calls.Load())
		}
	}
}

func TestIssue17GeneratedApplicationsDoNotFallBackToControllers(t *testing.T) {
	for _, direct := range []bool{false, true} {
		kernel, _ := newGeneratedSkeletonHTTP(t, "", false)
		for _, path := range []string{"/index/index/hello", "/admin/index/hello"} {
			response := serveGeneratedSkeletonRequest(t, kernel, http.MethodGet, path, direct, nil)
			if response.Code != http.StatusNotFound {
				t.Errorf("direct=%v %s: unregistered controller returned %d %s", direct, path, response.Code, response.Body.String())
			}
		}
		for _, path := range []string{"/", "/index/hello/Visitor", "/admin/hello/Visitor"} {
			response := serveGeneratedSkeletonRequest(t, kernel, http.MethodGet, path, direct, nil)
			if response.Code != http.StatusOK {
				t.Errorf("direct=%v %s: registered route returned %d %s", direct, path, response.Code, response.Body.String())
			}
		}
	}
}
