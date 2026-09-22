package openapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestCommentedAPIRoutes 验证简洁入口复用真实签名、完整路径及原有错误语义。
func TestCommentedAPIRoutes(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "接口", Version: "1"}, WithSourceComments(commentCatalog()))
	if err != nil {
		t.Fatal(err)
	}
	router := route.NewRouter()
	api := registry.Routes(router)
	for _, method := range []func(string, any) error{
		func(path string, callback any) error { return api.Get(path, callback) },
		func(path string, callback any) error { return api.Post(path, callback) },
		func(path string, callback any) error { return api.Put(path, callback) },
		func(path string, callback any) error { return api.Patch(path, callback) },
		func(path string, callback any) error { return api.Delete(path, callback) },
		func(path string, callback any) error { return api.Options(path, callback) },
	} {
		if err := method("/users/:id", commentHandler); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.Head("/users/:id", commentHandler); err == nil {
		t.Fatal("HEAD 入口跳过了响应体检查")
	}
	if err := api.Handle(Operation{Method: http.MethodPost, Path: "/create/:id", SuccessStatus: http.StatusCreated}, commentHandler); err != nil {
		t.Fatal(err)
	}
	if err := api.Get("/users/:id", commentHandler); !errors.Is(err, route.ErrDuplicateRoute) {
		t.Fatalf("重复路由错误丢失: %v", err)
	}
	if err := registry.ValidateRouter(router, "/"); err != nil {
		t.Fatal(err)
	}
	var absent *API
	if err := absent.Get("/", commentHandler); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("空 API 入口错误: %v", err)
	}
}
