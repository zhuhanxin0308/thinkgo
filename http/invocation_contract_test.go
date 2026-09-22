package http

import (
	"net/http"
	"net/http/httptest"
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
}
