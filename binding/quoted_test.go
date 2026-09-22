package binding

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestQuotedJSONKeepsLargeIdentifiers 验证 json,string 遵循 Go 的编码方式且验证解码后的真实值。
func TestQuotedJSONKeepsLargeIdentifiers(t *testing.T) {
	type input struct {
		ID     int64 `json:"id,string" validate:"gt:0" default:"1"`
		Nested struct {
			Value bool `json:"value,string"`
		} `json:"nested"`
	}
	var target input
	const body = `{"id":"9007199254740993","nested":{"value":"false"}}`
	if err := Bind(bindingRequest(t, body), &target); err != nil || target.ID != 9007199254740993 || target.Nested.Value {
		t.Fatalf("字符串数值绑定错误: %#v %v", target, err)
	}
	encoded, err := json.Marshal(target)
	if err != nil || string(encoded) != body {
		t.Fatalf("Go JSON 往返不一致: %s %v", encoded, err)
	}
	for _, body := range []string{`{"id":123}`, `{"id":"bad"}`, `{"id":"0"}`, `{"id":"1 2"}`} {
		if err := Bind(bindingRequest(t, body), &target); err == nil {
			t.Fatalf("非法编码被接受: %s", body)
		}
	}
	if err := Bind(bindingRequest(t, `{}`), &target); err != nil || target.ID != 1 {
		t.Fatalf("默认值不应按请求文本再次解码: %#v %v", target, err)
	}
	_, schemas, err := Operation(reflect.TypeFor[input](), nil, "quoted", 200)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schemas, &values); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, schema := range values {
		if id, ok := schema.Properties["id"]; ok {
			found = true
			if id["type"] != "string" || id["default"] != "1" || id["contentSchema"] == nil {
				t.Fatalf("字符串数值契约失配: %#v", id)
			}
		}
	}
	if !found {
		t.Fatal("缺少字符串数值契约")
	}
}
