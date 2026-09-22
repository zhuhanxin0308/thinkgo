package context

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const strictJSONCheckoutBody = `{"sequence":123456789,"customer_id":12345,"items":[{"product_id":123,"quantity":2},{"product_id":456,"quantity":3}]}`

// TestStrictJSONValueContract 验证数字精度、字符串兼容性、空集合和根节点语义。
func TestStrictJSONValueContract(t *testing.T) {
	tests := []struct {
		name string
		body string
		want any
	}{
		{"精确数字", `{"large":18446744073709551616,"decimal":-0.0012300e+12}`, map[string]any{"large": json.Number("18446744073709551616"), "decimal": json.Number("-0.0012300e+12")}},
		{"Unicode", `{"中文":"你好","escape":"\"\\\/\b\f\n\r\t","pair":"\uD834\uDD1E","single":"\uD800"}`, map[string]any{"中文": "你好", "escape": "\"\\/\b\f\n\r\t", "pair": "𝄞", "single": "�"}},
		{"无效UTF8", "{\"key\":\"\xff\xfe\"}", map[string]any{"key": "��"}},
		{"容器与空值", `{"empty":{},"array":[],"null":null,"true":true,"false":false}`, map[string]any{"empty": map[string]any{}, "array": []any{}, "null": nil, "true": true, "false": false}},
		{"转义分隔符", `{"a\"b":"x\\\"},[]","nested":[["value"]]}`, map[string]any{"a\"b": "x\\\"},[]", "nested": []any{[]any{"value"}}}},
		{"根数组", `[1,"two",null]`, []any{json.Number("1"), "two", nil}},
		{"根字符串", `"value"`, "value"},
		{"根数字", `-0`, json.Number("-0")},
		{"根布尔", `true`, true},
		{"根空值", `null`, nil},
		{"文档空白", " \n\r\t { \"a\" : [ 1 , 2 ] } \t\n", map[string]any{"a": []any{json.Number("1"), json.Number("2")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeStrictJSONValue([]byte(test.body))
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("严格 JSON 值不一致: got=%#v want=%#v err=%v", got, test.want, err)
			}
			_, objectErr := decodeStrictJSONObject([]byte(test.body))
			_, isObject := test.want.(map[string]any)
			if (objectErr == nil) != isObject {
				t.Fatalf("HTTP 参数对象根节点约束改变: err=%v", objectErr)
			}
		})
	}
}

// TestStrictJSONRejectsAmbiguousDocuments 确保无效输入不会生成可供业务使用的部分树。
func TestStrictJSONRejectsAmbiguousDocuments(t *testing.T) {
	tests := map[string]string{
		"空体":         " \t\r\n",
		"重复键":        `{"a":1,"a":2}`,
		"转义等价键":      `{"a":1,"\u0061":2}`,
		"嵌套重复键":      `{"outer":[{"role":"user","role":"admin"}]}`,
		"Unicode等价键": `{"𝄞":1,"\uD834\uDD1E":2}`,
		"替换字符等价键":    "{\"\xff\":1,\"\\ufffd\":2}",
		"尾随对象":       `{} {}`,
		"尾随标量":       `{} true`,
		"未闭合":        `{"value":[1,2}`,
		"错误转义":       `{"value":"\x41"}`,
		"错误Unicode":  `{"value":"\u12"}`,
		"控制字符":       "{\"value\":\"a\nb\"}",
		"错误数字":       `{"value":01}`,
		"不完整指数":      `{"value":1e+}`,
		"数组尾逗号":      `{"value":[1,]}`,
		"缺少冒号":       `{"value" true}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			value, err := decodeStrictJSONValue([]byte(body))
			if err == nil || value != nil {
				t.Fatalf("无效 JSON 必须返回错误且不暴露部分值: value=%#v err=%v", value, err)
			}
		})
	}
}

// TestStrictJSONDepthBoundary 保留从根值零层开始、标量同样计入层数的既有边界。
func TestStrictJSONDepthBoundary(t *testing.T) {
	for _, depth := range []int{maxJSONNestingDepth - 1, maxJSONNestingDepth, maxJSONNestingDepth + 1} {
		for _, object := range []bool{false, true} {
			prefix, suffix := "[", "]"
			if object {
				prefix, suffix = `{"nested":`, "}"
			}
			body := strings.Repeat(prefix, depth) + "0" + strings.Repeat(suffix, depth)
			_, err := decodeStrictJSONValue([]byte(body))
			if (err == nil) != (depth <= maxJSONNestingDepth) {
				t.Fatalf("嵌套边界错误: depth=%d object=%t err=%v", depth, object, err)
			}
		}
	}
	// 最深空容器本身也是一个值，不因没有子节点而绕过深度约束。
	for _, depth := range []int{maxJSONNestingDepth, maxJSONNestingDepth + 1} {
		body := strings.Repeat("[", depth) + "{}" + strings.Repeat("]", depth)
		_, err := decodeStrictJSONValue([]byte(body))
		if (err == nil) != (depth <= maxJSONNestingDepth) {
			t.Fatalf("空容器深度边界错误: depth=%d err=%v", depth, err)
		}
	}
}

type strictJSONCustomValue struct {
	Raw string
}

// UnmarshalJSON 记录原始表示，用于确认严格校验不会替换标准目标解码行为。
func (value *strictJSONCustomValue) UnmarshalJSON(body []byte) error {
	value.Raw = string(body)
	return nil
}

// TestStrictJSONTargetKeepsDecoderSemantics 验证自定义解码、字符串数字和接口数字类型。
func TestStrictJSONTargetKeepsDecoderSemantics(t *testing.T) {
	const body = `{"custom":{"value":1e+02},"quoted":"42","large":18446744073709551616}`
	type target struct {
		Custom strictJSONCustomValue `json:"custom"`
		Quoted int                   `json:"quoted,string"`
		Large  any                   `json:"large"`
	}
	for _, preparse := range []bool{false, true} {
		raw := httptest.NewRequest(http.MethodPost, "/json", strings.NewReader(body))
		raw.Header.Set("Content-Type", "application/json")
		request := newRequestForTest(t, raw)
		if preparse {
			if err := request.Parse(); err != nil {
				t.Fatal(err)
			}
		}
		var got target
		if err := request.Json(&got); err != nil || got.Custom.Raw != `{"value":1e+02}` || got.Quoted != 42 || got.Large != json.Number("18446744073709551616") {
			t.Fatalf("目标解码语义改变: preparse=%t got=%#v err=%v", preparse, got, err)
		}
	}
	var custom strictJSONCustomValue
	if err := decodeStrictJSONTarget([]byte(`{"a":1,"\u0061":2}`), &custom); err == nil || custom.Raw != "" {
		t.Fatalf("重复键必须在自定义解码器执行前被拒绝: custom=%#v err=%v", custom, err)
	}
	if err := decodeStrictJSONTarget([]byte(`[1,2]`), &custom); err != nil || custom.Raw != `[1,2]` {
		t.Fatalf("独立目标绑定必须保留根数组支持: custom=%#v err=%v", custom, err)
	}
}

// TestStrictJSONAllocationReduction 比较等价 Token 参考路径，确保热点修改实际降低分配。
func TestStrictJSONAllocationReduction(t *testing.T) {
	body := []byte(strictJSONCheckoutBody)
	current := testing.AllocsPerRun(100, func() {
		if _, err := decodeStrictJSONObject(body); err != nil {
			panic(err)
		}
	})
	reference := testing.AllocsPerRun(100, func() {
		if _, err := referenceStrictJSONValue(body); err != nil {
			panic(err)
		}
	})
	t.Logf("每请求分配: current=%.0f reference=%.0f", current, reference)
	if current >= reference {
		t.Fatalf("严格语义不变时必须低于 Token 参考路径的分配: current=%.0f reference=%.0f", current, reference)
	}
}

// FuzzStrictJSONMatchesTokenReference 用已发布的 Token 语义检查优化路径接受范围和结果。
func FuzzStrictJSONMatchesTokenReference(f *testing.F) {
	for _, body := range []string{strictJSONCheckoutBody, `{"a":1,"\u0061":2}`, `"\uD800"`, "\"\xff\"", `[{"a":true},null,1e1000]`, `{}`, `[]`, `null`, `{} {}`, `{"x":"a\\\"b"}`} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		got, gotErr := decodeStrictJSONValue(body)
		want, wantErr := referenceStrictJSONValue(body)
		if (gotErr == nil) != (wantErr == nil) || gotErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("严格解析与 Token 参考不一致: body=%q got=%#v gotErr=%v want=%#v wantErr=%v", body, got, gotErr, want, wantErr)
		}
	})
}

// referenceStrictJSONValue 保留发布前的 Token 路径，作为差分测试与分配基准的固定参照。
func referenceStrictJSONValue(body []byte) (any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("JSON 请求体不能为空")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := referenceReadJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON 请求体只能包含一个文档")
	}
	return value, nil
}

func referenceReadJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxJSONNestingDepth {
		return nil, fmt.Errorf("JSON 嵌套深度不能超过 %d", maxJSONNestingDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON 对象键必须是字符串")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("JSON 对象包含重复键 %q", key)
			}
			value, err := referenceReadJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := referenceReadJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("JSON 分隔符 %q 位置非法", delimiter)
	}
}
