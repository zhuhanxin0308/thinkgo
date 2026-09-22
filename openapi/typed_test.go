package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

type typedRequest struct {
	ID   int    `path:"id"`
	Name string `json:"name" validate:"required|length:2,32"`
}
type typedResponse struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TestTypedOperationsReuseValidatedSchemas 验证多个操作复用组件，文档完整解析内部引用并保持不可变。
func TestTypedOperationsReuseValidatedSchemas(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "Typed API", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[typedRequest, typedResponse](registry, "PUT", "/users/:id", "updateUser", 200); err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[typedRequest, typedResponse](registry, "POST", "/users/:id", "createUser", 201); err != nil {
		t.Fatal(err)
	}
	content, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(content) || !strings.Contains(string(content), "minLength") || !strings.Contains(string(content), "/users/{id}") {
		t.Fatalf("契约未完整输出: %s", content)
	}
	if err := RegisterTyped[typedRequest, typedResponse](registry, "PATCH", "/users/:id", "patchUser", 200); err != ErrRegistryFrozen {
		t.Fatalf("冻结后注册错误: %v", err)
	}
}

// TestTypedRegistrationIsAtomic 验证组件冲突不会留下操作、名称占用或孤立组件。
func TestTypedRegistrationIsAtomic(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	operation, schemas, err := typedOperation(reflect.TypeFor[typedRequest](), reflect.TypeFor[typedResponse](), "update", 200)
	if err != nil {
		t.Fatal(err)
	}
	var conflictingName string
	for name := range schemas {
		conflictingName = name
		break
	}
	if err := registry.AddSchema(conflictingName, &openapi3.SchemaRef{Value: openapi3.NewBoolSchema()}); err != nil {
		t.Fatal(err)
	}
	if err := registry.register("PUT", "/users/:id", operation, schemas); !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("组件冲突未拒绝: %v", err)
	}
	if len(registry.operations) != 0 || len(registry.operationIDs) != 0 || len(registry.document.Components.Schemas) != 1 || registry.document.Paths.Len() != 0 {
		t.Fatal("失败注册留下部分状态")
	}
	if err := registry.Register("GET", "/health", validOperation("update")); err != nil {
		t.Fatalf("失败注册占用了名称: %v", err)
	}
}

// TestRegistryRejectsExternalSchemaReferences 验证解析器只使用文档内组件，不加载外部地址或文件。
func TestRegistryRejectsExternalSchemaReferences(t *testing.T) {
	for _, reference := range []string{"https://example.invalid/schema.json", "file:///private/schema.json"} {
		registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
		if err := registry.AddSchema("External", &openapi3.SchemaRef{Ref: reference}); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.JSON(context.Background()); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("外部引用未拒绝: %v", err)
		}
		if registry.frozen {
			t.Fatal("校验失败不能冻结注册表")
		}
	}
}

// TestQuotedTypedContractPassesDocumentValidation 验证字符串大整数契约可由完整 OpenAPI 加载器解析与发布。
func TestQuotedTypedContractPassesDocumentValidation(t *testing.T) {
	type input struct {
		ID int64 `json:"id,string" validate:"required|gt:0"`
	}
	registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	if err := RegisterTyped[input, typedResponse](registry, "POST", "/users", "create", 201); err != nil {
		t.Fatal(err)
	}
	encoded, err := registry.JSON(context.Background())
	if err != nil || !strings.Contains(string(encoded), "contentSchema") {
		t.Fatalf("字符串数值文档无效: %s %v", encoded, err)
	}
}
