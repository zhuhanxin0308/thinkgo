package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/openapi"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

type businessRepository interface{ UserName() string }
type memoryBusinessRepository struct{}

func (*memoryBusinessRepository) UserName() string { return "Ada" }

type repositoryController struct{}

func (*repositoryController) Show(repository businessRepository) string { return repository.UserName() }

// TestAllHTTPHandlersInjectServiceInterfaces 验证控制器、普通回调与 OpenAPI 共用服务接口分类。
func TestAllHTTPHandlersInjectServiceInterfaces(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := application.Instance("businessRepository", &memoryBusinessRepository{}); err != nil {
		t.Fatal(err)
	}
	if err := application.BindFactory("Repository", func() interface{} { return &repositoryController{} }); err != nil {
		t.Fatal(err)
	}
	host := newTestHTTPHandler(t, application)
	router := route.NewRouter()
	for _, callback := range []any{"Repository@Show", func(repository businessRepository) string { return repository.UserName() }} {
		registered, err := router.Get("/users", callback)
		if err != nil {
			t.Fatal(err)
		}
		response := host.dispatch(registered, fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/users", nil)))
		if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "Ada" {
			t.Fatalf("服务接口被当成请求参数: %d %s", response.GetStatus(), response.GetBody())
		}
		router = route.NewRouter()
	}
	registry, err := openapi.NewRegistry(openapi3.Info{Title: "用户", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := openapi.Handle(router, registry, openapi.Operation{Method: http.MethodGet, Path: "/api/users", OperationID: "users.list"}, func(repository businessRepository) string { return repository.UserName() }); err != nil {
		t.Fatalf("OpenAPI 拒绝合法服务接口: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/api/users", nil))
	registered, _, err := router.Match(request)
	if err != nil || registered == nil {
		t.Fatalf("JSON 路由匹配失败: %v", err)
	}
	response := host.dispatch(registered, request)
	if !strings.HasPrefix(response.GetHeader("Content-Type"), "application/json") || string(response.GetBody()) != `"Ada"` {
		t.Fatalf("字符串 JSON 路由的实际响应错误: %s %q", response.GetHeader("Content-Type"), response.GetBody())
	}
	content, err := registry.JSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	document, err := openapi3.NewLoader().LoadFromData(content)
	if err != nil {
		t.Fatal(err)
	}
	media := document.Paths.Value("/api/users").Get.Responses.Status(http.StatusOK).Value.Content
	if len(media) != 1 || media["application/json"] == nil {
		t.Fatalf("契约媒体类型与实际响应不同: %v", media)
	}
}
