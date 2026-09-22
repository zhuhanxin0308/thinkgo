package binding

import (
	"encoding/json"
	"reflect"
	"strings"
)

// unquote 在类型转换和规则验证前处理 json,string，保留完整数值精度。
func (state *bindState) unquote(raw any, path string) (any, bool) {
	if raw == nil {
		return nil, true
	}
	text, ok := raw.(string)
	if !ok || !json.Valid([]byte(text)) {
		state.fail(path, "type", "字段必须是包含单一 JSON 值的字符串")
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		state.fail(path, "json", "字段内的 JSON 编码无效")
		return nil, false
	}
	return value, true
}

// quotedSchema 描述字符串中的 JSON 内容，具体值的规则仍由运行时完整执行。
func quotedSchema(schema map[string]any, item field) map[string]any {
	if !item.quoted {
		return schema
	}
	result := map[string]any{"type": "string", "contentMediaType": "application/json", "contentSchema": schema}
	if item.node.typ.Kind() == reflect.Pointer && !ruleRequired(item.rules) {
		result["type"] = []string{"string", "null"}
	}
	if item.description != "" {
		result["description"] = item.description
	}
	if item.rules != "" {
		result["x-thinkgo-validation"] = item.rules
	}
	if item.hasDefault {
		if item.defaultValue == nil {
			result["default"] = nil
		} else {
			encoded, _ := json.Marshal(item.defaultValue)
			result["default"] = string(encoded)
		}
	}
	return result
}
