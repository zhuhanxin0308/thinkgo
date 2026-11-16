package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

type contentTypeViewController struct{ framework.Controller }

func (c *contentTypeViewController) Page() *fwcontext.Response {
	return fwcontext.NewResponse().HTML(c.Fetch("page", map[string]any{"Title": "控制器视图"}))
}

func TestControllerDispatchSendsExplicitlyRenderedViewAsHTML(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join(base, "app", "view")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "page.html"), []byte("<main>{{.Title}}</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newTestHTTPApp(t, base, map[string]any{"enable": false})
	app.BindFactory("ContentTypeView", func() any { return &contentTypeViewController{} })
	if _, err := mustHTTPRoute(t, app).Get("/page", "ContentTypeView@Page"); err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/page", nil))
	if mime := writer.Result().Header.Get("Content-Type"); writer.Code != http.StatusOK || mime != "text/html; charset=utf-8" || writer.Body.String() != "<main>控制器视图</main>" {
		t.Fatalf("控制器渲染响应错误: status=%d type=%q body=%q", writer.Code, mime, writer.Body.String())
	}
}
