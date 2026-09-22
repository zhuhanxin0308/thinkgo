package openapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

type handleInput struct {
	binding.Input
	ID   int    `path:"id" validate:"gt:0"`
	Name string `json:"name" validate:"required"`
}

type handleService struct{}

// TestHandleDerivesContractFromActualCallback 验证真实签名、分组路径和元信息共同生成契约。
func TestHandleDerivesContractFromActualCallback(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"})
	router := route.NewRouter()
	metadata := Operation{Method: http.MethodPost, Path: "/users/:id", OperationID: "create", SuccessStatus: http.StatusCreated, Tags: []string{"用户"}, Summary: "创建用户"}
	if err := router.Group("/api", func(group *route.Group) error {
		return Handle(group, registry, metadata, func(input *handleInput, service *handleService, request *fwcontext.Request) (typedResponse, error) {
			return typedResponse{ID: input.ID, Name: input.Name}, nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	metadata.Tags[0] = "已修改"
	if err := registry.ValidateRouter(router, "/api"); err != nil {
		t.Fatal(err)
	}
	encoded, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(encoded)
	if err != nil {
		t.Fatal(err)
	}
	operation := document.Paths.Value("/api/users/{id}").Post
	if operation.Tags[0] != "用户" || operation.Summary != "创建用户" || operation.Responses.Status(http.StatusCreated) == nil || operation.Parameters[0].Value.Name != "id" {
		t.Fatalf("签名契约错误: %s", encoded)
	}
}

// TestHandleRejectsMismatchWithoutPartialRegistration 验证错误定义不会占用路径、操作名或组件。
func TestHandleRejectsMismatchWithoutPartialRegistration(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		status  int
		handler any
	}{
		{"缺少路径字段", "/users/:other", http.StatusOK, func(handleInput) typedResponse { return typedResponse{} }},
		{"多余路径字段", "/users", http.StatusOK, func(handleInput) typedResponse { return typedResponse{} }},
		{"标量参数", "/users/:id", http.StatusOK, func(int) typedResponse { return typedResponse{} }},
		{"重复请求", "/users/:id", http.StatusOK, func(handleInput, *handleInput) typedResponse { return typedResponse{} }},
		{"动态返回", "/users", http.StatusOK, func() any { return nil }},
		{"原始响应", "/users", http.StatusOK, func() *fwcontext.Response { return nil }},
		{"无内容状态携带返回值", "/users", http.StatusNoContent, func() typedResponse { return typedResponse{} }},
		{"非法状态", "/users", http.StatusBadRequest, func() typedResponse { return typedResponse{} }},
		{"字符串处理器", "/users", http.StatusOK, "Users@Read"},
		{"空函数", "/users", http.StatusOK, (func() typedResponse)(nil)},
		{"可变参数", "/users", http.StatusOK, func(...string) typedResponse { return typedResponse{} }},
		{"花括号路径", "/users/{id}", http.StatusOK, func(handleInput) typedResponse { return typedResponse{} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			registry, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"})
			router := route.NewRouter()
			metadata := Operation{Method: http.MethodGet, Path: test.path, OperationID: "read", SuccessStatus: test.status}
			if err := Handle(router, registry, metadata, test.handler); err == nil {
				t.Fatal("错误契约未被拒绝")
			}
			if len(registry.operations) != 0 || len(registry.document.Components.Schemas) != 0 {
				t.Fatal("失败留下了契约")
			}
			metadata.Path = "/users"
			metadata.SuccessStatus = http.StatusOK
			if err := Handle(router, registry, metadata, func() typedResponse { return typedResponse{} }); err != nil {
				t.Fatalf("失败占用了注册状态: %v", err)
			}
			if err := registry.ValidateRouter(router, "/"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestHandleFreezingAndConflicts 验证路由、组件及 operationId 冲突均保持两边状态一致。
func TestHandleFreezingAndConflicts(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"})
	router := route.NewRouter()
	metadata := Operation{Method: http.MethodGet, Path: "/one", OperationID: "read"}
	handler := func() typedResponse { return typedResponse{} }
	if err := Handle(router, registry, metadata, handler); err != nil {
		t.Fatal(err)
	}
	metadata.Path = "/two"
	if err := Handle(router, registry, metadata, handler); !errors.Is(err, ErrDuplicateOperationID) {
		t.Fatalf("重复操作错误: %v", err)
	}
	metadata.OperationID = "readTwo"
	if err := Handle(router, registry, metadata, handler); err != nil {
		t.Fatalf("失败路由无法重试: %v", err)
	}
	if _, err := registry.JSON(context.Background()); err != nil {
		t.Fatal(err)
	}
	metadata.Path, metadata.OperationID = "/three", "readThree"
	if err := Handle(router, registry, metadata, handler); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("冻结文档注册错误: %v", err)
	}
	if err := registry.ValidateRouter(router, "/"); err != nil {
		t.Fatalf("冻结失败留下了路由: %v", err)
	}
	other, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"})
	if err := Handle(router, other, metadata, handler); !errors.Is(err, route.ErrRouterFrozen) || len(other.operations) != 0 {
		t.Fatalf("冻结路由留下了文档: %v", err)
	}
}

// TestHandleConcurrentRegistration 验证重复注册与不同注册使用一致的路由、文档锁顺序。
func TestHandleConcurrentRegistration(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"})
	router := route.NewRouter()
	var wait sync.WaitGroup
	const registrations = 12
	for range registrations {
		wait.Go(func() {
			err := Handle(router, registry, Operation{Method: http.MethodDelete, Path: "/users", OperationID: "delete"}, func() error { return nil })
			if err != nil && !errors.Is(err, route.ErrDuplicateRoute) {
				t.Errorf("并发注册错误: %v", err)
			}
		})
	}
	wait.Wait()
	if err := registry.ValidateRouter(router, "/"); err != nil {
		t.Fatal(err)
	}
	encoded, err := registry.JSON(context.Background())
	if err != nil || !strings.Contains(string(encoded), `"204"`) {
		t.Fatalf("空返回未推导无内容状态: %s %v", encoded, err)
	}
}
