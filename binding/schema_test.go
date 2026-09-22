package binding

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestOperationDescribesSameInputContract 验证参数来源、默认值和嵌套约束来自绑定使用的同一份定义。
func TestOperationDescribesSameInputContract(t *testing.T) {
	operationJSON, schemas, err := Operation(reflect.TypeOf(createInput{}), reflect.TypeOf(itemInput{}), "createUser", 201)
	if err != nil {
		t.Fatal(err)
	}
	var operation openapi3.Operation
	if err := json.Unmarshal(operationJSON, &operation); err != nil {
		t.Fatal(err)
	}
	if len(operation.Parameters) != 3 || operation.RequestBody == nil || operation.Responses.Value("201") == nil {
		t.Fatalf("接口结构不完整: %#v", operation)
	}
	encoded, err := json.Marshal(schemas)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range document {
		object := raw.(map[string]any)
		properties, _ := object["properties"].(map[string]any)
		if name, exists := properties["name"].(map[string]any); exists {
			found = true
			if name["minLength"] != float64(2) || name["maxLength"] != float64(32) || object["additionalProperties"] != false {
				t.Fatalf("字段约束失配: %#v", object)
			}
		}
	}
	if !found {
		t.Fatal("请求体 Schema 缺失")
	}
}

type schemaContractInput struct {
	Name    string         `json:"name" validate:"required|length:2,8"`
	Count   uint8          `json:"count" validate:"between:1,10"`
	Enabled Optional[bool] `json:"enabled"`
	Tree    *TreeInput     `json:"tree"`
}

// TestGeneratedSchemaMatchesBinding 验证递归引用、空值、数值范围和必填规则与实际请求结果一致。
func TestGeneratedSchemaMatchesBinding(t *testing.T) {
	operation, schemas, err := Operation(reflect.TypeFor[schemaContractInput](), reflect.TypeFor[schemaContractInput](), "contract", 200)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{"openapi": "3.1.0", "info": map[string]string{"title": "API", "version": "1"}, "paths": map[string]any{"/contract": map[string]any{"post": operation}}, "components": map[string]any{"schemas": schemas}}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := openapi3.NewLoader().LoadFromData(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	schema := loaded.Paths.Value("/contract").Post.RequestBody.Value.Content["application/json"].Schema.Value
	for _, body := range []string{`{"name":"Ada","count":1,"enabled":false,"tree":{"value":1,"next":{"value":2}}}`, `{"name":"Ada","enabled":null}`, `{}`, `{"name":"x"}`, `{"name":"Ada","count":11}`, `{"name":"Ada","extra":true}`, `{"name":null}`} {
		var value any
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			t.Fatal(err)
		}
		var input schemaContractInput
		bindErr := Bind(bindingRequest(t, body), &input)
		schemaErr := schema.VisitJSON(value, openapi3.EnableJSONSchema2020())
		if (bindErr == nil) != (schemaErr == nil) {
			t.Fatalf("文档与执行结果分歧 %s: bind=%v schema=%v", body, bindErr, schemaErr)
		}
	}
}
