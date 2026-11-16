package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestSPAFallbackRejectsMissingAssetPaths(t *testing.T) {
	base := t.TempDir()
	const index = "<main>SPA 入口</main>"
	public := filepath.Join(base, "public")
	if err := os.MkdirAll(public, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "existing.js"), []byte("real asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newTestHTTPApp(t, base, map[string]interface{}{"enable": false})
	if _, err := mustHTTPRoute(t, app).Get("/release/3.0.8", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("版本说明")
	}); err != nil {
		t.Fatal(err)
	}
	host := newTestHTTPHandler(t, app)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/dashboard", index, http.StatusOK},
		{http.MethodGet, "/prefix.with.dot/dashboard", index, http.StatusOK},
		{http.MethodGet, "/existing.js", "real asset", http.StatusOK},
		{http.MethodGet, "/release/3.0.8", "版本说明", http.StatusOK},
		{http.MethodGet, "/release/3.0.9", "404 Not Found", http.StatusNotFound},
		{http.MethodGet, "/assets/missing.js", "404 Not Found", http.StatusNotFound},
		{http.MethodGet, "/missing.css?version=2", "404 Not Found", http.StatusNotFound},
		{http.MethodGet, "/missing.png", "404 Not Found", http.StatusNotFound},
		{http.MethodGet, "/missing.svg/", "404 Not Found", http.StatusNotFound},
		{http.MethodHead, "/missing.js", "", http.StatusNotFound},
		{http.MethodHead, "/dashboard", "", http.StatusOK},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			writer := httptest.NewRecorder()
			host.ServeHTTP(writer, httptest.NewRequest(test.method, "http://127.0.0.1"+test.path, nil))
			if writer.Code != test.status || writer.Body.String() != test.body {
				t.Fatalf("SPA 回退结果错误: %d, %q", writer.Code, writer.Body.String())
			}
			if test.body == index && !strings.HasPrefix(writer.Result().Header.Get("Content-Type"), "text/html") {
				t.Fatal("合法 SPA 页面仍应返回 HTML 类型")
			}
		})
	}
}
