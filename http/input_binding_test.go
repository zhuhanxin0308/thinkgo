package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

type automaticInput struct {
	binding.Input
	ID      int                    `path:"id" validate:"gt:0"`
	Name    string                 `json:"name" validate:"required|length:2,32"`
	Enabled binding.Optional[bool] `json:"enabled"`
}
type automaticInputService struct {
	Prefix string `json:"prefix"`
}
type automaticInputController struct{}

func (*automaticInputController) Update(input *automaticInput, service *automaticInputService) (map[string]any, error) {
	if input.ID == 404 {
		return nil, exception.NewHttpException(http.StatusNotFound, "记录不存在")
	}
	return map[string]any{"id": input.ID, "name": service.Prefix + input.Name, "enabled": input.Enabled.Value(), "present": input.Enabled.IsSet()}, nil
}

// TestInputArgumentsShareControllerAndCallbackContract 验证控制器与回调自动绑定使用相同字段契约并保留服务注入。
func TestInputArgumentsShareControllerAndCallbackContract(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := application.BindFactory("Automatic", func() *automaticInputController { return &automaticInputController{} }); err != nil {
		t.Fatal(err)
	}
	if err := application.BindFactory("automaticInputService", func() *automaticInputService { return &automaticInputService{Prefix: "service:"} }); err != nil {
		t.Fatal(err)
	}
	host := newTestHTTPHandler(t, application)
	for _, handler := range []any{"automatic/update", func(input automaticInput, service *automaticInputService) (map[string]any, error) {
		return (&automaticInputController{}).Update(&input, service)
	}} {
		router := route.NewRouter()
		if _, err := router.Patch("/users/:id", handler); err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			id, body string
			status   int
		}{{"1", `{"name":"Ada","enabled":false}`, 200}, {"1", `{"name":"x"}`, 422}, {"abc", `{"name":"Ada"}`, 400}, {"404", `{"name":"Ada"}`, 404}} {
			raw := httptest.NewRequest(http.MethodPatch, "http://example.com/users/"+test.id, strings.NewReader(test.body))
			raw.Header.Set("Content-Type", "application/json")
			raw.Header.Set("Accept", "application/json")
			request := fwcontext.MustNewRequest(raw)
			matched, parameters, err := router.Match(request)
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range parameters {
				request.SetRoute(name, value)
			}
			response := host.dispatch(matched, request)
			if response.GetStatus() != test.status {
				t.Fatalf("自动绑定错误: status=%d body=%s", response.GetStatus(), response.GetBody())
			}
			if test.status == http.StatusOK {
				var result map[string]any
				if err := json.Unmarshal(response.GetBody(), &result); err != nil || result["name"] != "service:Ada" || result["present"] != true || result["enabled"] != false {
					t.Fatalf("服务或三态绑定失效: %#v %v", result, err)
				}
			}
		}
	}
}
