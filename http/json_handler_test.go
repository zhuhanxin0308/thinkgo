package http

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestJSONHandlerUsesDeclaredSuccessAndExistingErrors 验证声明状态只用于成功出口，错误继续保留统一语义。
func TestJSONHandlerUsesDeclaredSuccessAndExistingErrors(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	host := newTestHTTPHandler(t, application)
	type output struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name     string
		callback any
		declared int
		status   int
		body     string
	}{
		{"创建", func() output { return output{Name: "Ada"} }, http.StatusCreated, http.StatusCreated, `{"name":"Ada"}`},
		{"JSON字符串", func() string { return "hello" }, http.StatusAccepted, http.StatusAccepted, `"hello"`},
		{"空指针", func() *output { return nil }, http.StatusOK, http.StatusOK, `null`},
		{"空成功", func() error { return nil }, 0, http.StatusNoContent, ""},
		{"无返回", func() {}, 0, http.StatusNoContent, ""},
		{"业务失败", func() (output, error) { return output{}, exception.NewHttpException(http.StatusNotFound, "missing") }, http.StatusCreated, http.StatusNotFound, "missing"},
		{"内部失败", func() error { return errors.New("private") }, http.StatusNoContent, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)},
		{"编码失败", func() map[string]any { return map[string]any{"invalid": make(chan int)} }, http.StatusCreated, http.StatusInternalServerError, `{"message":"internal server error"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callback, err := route.NewJSONHandler(test.callback, test.declared)
			if err != nil {
				t.Fatal(err)
			}
			registered, err := route.NewRouter().Get("/json", callback)
			if err != nil {
				t.Fatal(err)
			}
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/json", nil))
			response := host.dispatch(registered, request)
			if response.GetStatus() != test.status || string(response.GetBody()) != test.body {
				t.Fatalf("响应不符合声明: %d %s", response.GetStatus(), response.GetBody())
			}
		})
	}
}

// TestJSONHandlerRetainsInputAndServiceInjection 验证统一注册仍使用当前应用和请求的参数绑定计划。
func TestJSONHandlerRetainsInputAndServiceInjection(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	application.BindFactory("thinkPHPRouteCallbackService", func() interface{} { return &thinkPHPRouteCallbackService{Prefix: "user-"} })
	host := newTestHTTPHandler(t, application)
	type input struct {
		binding.Input
		ID int `path:"id" validate:"gt:0"`
	}
	callback, err := route.NewJSONHandler(func(value input, request *fwcontext.Request, app *framework.App, service *thinkPHPRouteCallbackService) (map[string]any, error) {
		if request == nil || app != application || service == nil {
			return nil, errors.New("作用域注入不一致")
		}
		return map[string]any{"id": value.ID, "prefix": service.Prefix}, nil
	}, http.StatusCreated)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := route.NewRouter().Get("/users/:id", callback)
	if err != nil {
		t.Fatal(err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/users/8", nil))
	request.SetRoute("id", "8")
	response := host.dispatch(registered, request)
	if response.GetStatus() != http.StatusCreated || string(response.GetBody()) != `{"id":8,"prefix":"user-"}` {
		t.Fatalf("注入错误: %d %s", response.GetStatus(), response.GetBody())
	}
	request.SetRoute("id", "invalid")
	if response := host.dispatch(registered, request); response.GetStatus() != http.StatusBadRequest {
		t.Fatalf("绑定错误被成功状态覆盖: %d", response.GetStatus())
	}
}
