package binding

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

type privateEmbeddedInput struct {
	Value string `json:"value"`
}

// TestUnexportedAnonymousInputsFailBeforeBinding 验证具名 JSON 和其他来源不能绕过匿名字段的可写性检查。
func TestUnexportedAnonymousInputsFailBeforeBinding(t *testing.T) {
	for _, input := range []any{
		struct {
			Input
			*privateEmbeddedInput `json:"hidden"`
		}{},
		struct {
			Input
			privateEmbeddedInput `json:"hidden"`
		}{},
	} {
		typ := reflect.TypeOf(input)
		if err := ValidateType(typ); !errors.Is(err, ErrDefinition) {
			t.Errorf("未导出的匿名字段必须在预编译时拒绝: %v: %v", typ, err)
		}
		target := reflect.New(typ).Interface()
		if err := Bind(bindingRequest(t, `{"hidden":{"value":"x"}}`), target); !errors.Is(err, ErrDefinition) {
			t.Errorf("绑定必须返回定义错误而非 panic: %v", err)
		}
	}
}

type EmbeddedResponseIdentity struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

type EmbeddedResponseDetails struct {
	*EmbeddedResponseIdentity
	Region string `json:"region"`
}

type embeddedResponse struct {
	*EmbeddedResponseDetails
	Name string `json:"name"`
}

// TestEmbeddedPointerResponseMatchesActualJSON 验证匿名指针的缺省传播及非空时的成组必填契约。
func TestEmbeddedPointerResponseMatchesActualJSON(t *testing.T) {
	operation, schemas, err := Operation(reflect.TypeFor[Input](), reflect.TypeFor[embeddedResponse](), "embedded", 200)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{"openapi": "3.1.0", "info": map[string]string{"title": "API", "version": "1"}, "paths": map[string]any{"/embedded": map[string]any{"get": operation}}, "components": map[string]any{"schemas": schemas}}
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
	schema := loaded.Paths.Value("/embedded").Get.Responses.Value("200").Value.Content["application/json"].Schema.Value
	for _, output := range []embeddedResponse{
		{Name: "empty"},
		{Name: "region", EmbeddedResponseDetails: &EmbeddedResponseDetails{Region: "east"}},
		{Name: "identity", EmbeddedResponseDetails: &EmbeddedResponseDetails{EmbeddedResponseIdentity: &EmbeddedResponseIdentity{ID: 7, Kind: "user"}}},
	} {
		body, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatal(err)
		}
		if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
			t.Errorf("真实响应不符合生成契约: %s: %v", body, err)
		}
	}
	for _, body := range []string{`{}`, `{"name":"x","id":7}`, `{"name":"x","region":"east","id":7}`, `{"name":"x","note":"present"}`} {
		var value any
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			t.Fatal(err)
		}
		if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
			t.Errorf("无法由响应类型编码的字段组合被接受: %s", body)
		}
	}
}
