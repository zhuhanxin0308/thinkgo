package openapi

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type typedPageInput struct {
	binding.Input
	Page int `query:"page" default:"1" validate:"gt:0"`
}

// TestTypedPaginationSchemas 验证分页容器保留元素字段，游标动态值不会抹掉列表结构。
func TestTypedPaginationSchemas(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "分页接口", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[typedPageInput, db.Paginator[typedResponse]](registry, "GET", "/users", "users", 200); err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[typedPageInput, db.CursorPage[typedResponse]](registry, "GET", "/users/cursor", "cursorUsers", 200); err != nil {
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
	for _, path := range []string{"/users", "/users/cursor"} {
		response := document.Paths.Value(path).Get.Responses.Status(200).Value
		schema := response.Content["application/json"].Schema.Value
		list := schema.Properties["list"].Value
		if list.Items == nil || list.Items.Value.Properties["name"].Value.Type == nil || !list.Items.Value.Properties["name"].Value.Type.Is("string") {
			t.Fatalf("列表元素被降级为无类型对象: %#v", list)
		}
		if schema.Properties["page_size"] == nil || schema.Properties["has_more"] == nil {
			t.Fatalf("分页元信息缺失: %#v", schema.Properties)
		}
	}
	// 输出动态游标不应放宽请求定义，未知类型输入仍在启动期拒绝。
	type invalidInput struct {
		binding.Input
		Cursor any `json:"cursor"`
	}
	if err := binding.ValidateType(reflect.TypeFor[invalidInput]()); !errors.Is(err, binding.ErrDefinition) {
		t.Fatalf("请求字段被意外放宽: %v", err)
	}
}
