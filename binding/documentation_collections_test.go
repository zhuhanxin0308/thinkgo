package binding

import (
	"encoding/json"
	"reflect"
	"testing"
)

type documentedCollections struct {
	Input
	Pointer *struct {
		City string `json:"city" validate:"required"`
	} `json:"pointer"`
	List []struct {
		City string `json:"city"`
	} `json:"list"`
	Array [2]struct {
		City string `json:"city"`
	} `json:"array"`
	Map map[string]struct {
		City string `json:"city"`
	} `json:"map"`
	Patch Optional[struct {
		City string `json:"city"`
	}] `json:"patch"`
}

// TestDocumentedCollectionsPreserveSchema 验证补充集合与可空对象说明时不改变类型、长度和验证约束。
func TestDocumentedCollectionsPreserveSchema(t *testing.T) {
	typ := reflect.TypeFor[documentedCollections]()
	inline := make(map[string]TypeDocumentation)
	for _, name := range []string{"Pointer", "List", "Array", "Map", "Patch"} {
		inline[name] = TypeDocumentation{Fields: map[string]string{"City": name + "城市"}}
	}
	docs := Documentation{typ.PkgPath() + "." + typ.Name(): {Inline: inline}}
	operation, components, err := OperationWithDocumentation(typ, nil, "collections", 204, docs)
	if err != nil {
		t.Fatal(err)
	}
	var document, schemas map[string]any
	if err := json.Unmarshal(operation, &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(components, &schemas); err != nil {
		t.Fatal(err)
	}
	requestSchema := document["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	reference := requestSchema["$ref"].(string)
	properties := schemas[reference[len("#/components/schemas/"):]].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"pointer", "list", "array", "map", "patch"} {
		schema := properties[name].(map[string]any)
		var child map[string]any
		switch name {
		case "pointer", "patch":
			choices := schema["anyOf"].([]any)
			if choices[1].(map[string]any)["type"] != "null" {
				t.Fatal("可空对象语义丢失")
			}
			child = choices[0].(map[string]any)
		case "array", "list":
			child = schema["items"].(map[string]any)
			if name == "array" && (schema["minItems"] != float64(2) || schema["maxItems"] != float64(2) || schema["type"] != "array") {
				t.Fatal("定长数组约束丢失")
			}
		case "map":
			child = schema["additionalProperties"].(map[string]any)
		}
		city := child["properties"].(map[string]any)["city"].(map[string]any)
		if city["description"] == nil || city["type"] != "string" {
			t.Fatalf("集合子字段的说明或类型丢失: %s %#v", name, city)
		}
		if name == "pointer" && (city["not"] == nil || child["required"].([]any)[0] != "city") {
			t.Fatal("嵌套必填约束丢失")
		}
	}
}
