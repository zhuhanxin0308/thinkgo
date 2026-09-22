package openapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestHandleBoundariesRetainRegistrationState 验证宿主错误、路由域名及不支持的方法不会留下契约。
func TestHandleBoundariesRetainRegistrationState(t *testing.T) {
	metadata := Operation{Method: http.MethodGet, Path: "/users", OperationID: "read"}
	callback := func() string { return "ok" }
	registry, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"})
	if err := Handle(nil, registry, metadata, callback); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("空目标错误: %v", err)
	}
	for _, absent := range []*Registry{nil, {}} {
		if err := Handle(route.NewRouter(), absent, metadata, callback); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("空文档错误: %v", err)
		}
	}
	var absent *route.Router
	if err := Handle(absent, registry, metadata, callback); !errors.Is(err, route.ErrInvalidRoute) {
		t.Fatalf("空路由器错误: %v", err)
	}
	for _, method := range []string{"ANY", http.MethodHead} {
		metadata.Method = method
		if err := Handle(route.NewRouter(), registry, metadata, callback); err == nil {
			t.Fatalf("方法 %s 未拒绝", method)
		}
	}
	metadata.Method = http.MethodGet
	router := route.NewRouter()
	if err := router.Domain("api.example.com", func(group *route.Group) error {
		return Handle(group, registry, metadata, callback)
	}); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("域名契约错误: %v", err)
	}
	if err := router.Group("/api/{group}", func(group *route.Group) error {
		return Handle(group, registry, metadata, callback)
	}); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("分组参数契约错误: %v", err)
	}
	if err := Handle(router, registry, metadata, callback); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateRouter(router, "/"); err != nil {
		t.Fatal(err)
	}
	content, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	document, err := openapi3.NewLoader().LoadFromData(content)
	if err != nil || document.Paths.Value("/users").Get.Responses.Status(http.StatusOK).Value.Content.Get("application/json") == nil {
		t.Fatalf("字符串契约未对应 JSON 输出: %s %v", content, err)
	}
}
