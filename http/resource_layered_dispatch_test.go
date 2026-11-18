package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

type layeredAPIItemController struct {
	framework.Controller
}

func (c *layeredAPIItemController) Index() string { return "index" }
func (c *layeredAPIItemController) Read(id string) string {
	return "read:" + id + ":" + c.Request.Layer() + ":" + c.Request.Controller() + ":" + c.Request.Action()
}
func (c *layeredAPIItemController) Edit(id string) string { return "edit:" + id }

// TestIssue580ResourceLayeredControllerDispatch 验证默认路由配置下分层控制器 Resource 注册可端到端精确分发。
func TestIssue580ResourceLayeredControllerDispatch(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := app.BindFactory("Api.Item", func() interface{} { return &layeredAPIItemController{} }); err != nil {
		t.Fatal(err)
	}
	app.Route().Resource("/items", "api.Item")
	handler := newTestHTTPHandler(t, app)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/items", "index"},
		{"/items/42", "read:42:api:api.Item:read"},
		{"/items/42/edit", "edit:42"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com"+tc.path, nil))
		if rec.Code != stdhttp.StatusOK || rec.Body.String() != tc.want {
			t.Fatalf("GET %s 分发错误: status=%d body=%q want=%q", tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}
}
