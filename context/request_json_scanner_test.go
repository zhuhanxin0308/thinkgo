package context

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestStrictJSONSyntaxErrorPriority 保留完整文档语法错误优先于重复键与深度错误的既有顺序和位置。
func TestStrictJSONSyntaxErrorPriority(t *testing.T) {
	deep := strings.Repeat("[", maxJSONNestingDepth+1) + "0" + strings.Repeat("]", maxJSONNestingDepth+1)
	for _, body := range []string{
		`{"x":1,"x":2,}`, `{"x":1,"x":2} garbage`, deep + "!", "01", "-.1", "1e", "true false",
		`{"x":"\uZZZZ"}`, "{\"x\":\"\x00\"}", `[]]`, `{"x":01}`, `{"x":truefalse}`, `{"x"::1}`,
	} {
		var raw json.RawMessage
		want := json.Unmarshal([]byte(body), &raw)
		if want == nil {
			t.Fatalf("测试输入应为非法 JSON: %q", body)
		}
		_, got := decodeStrictJSONValue([]byte(body))
		if got == nil || reflect.TypeOf(got) != reflect.TypeOf(want) || got.Error() != want.Error() {
			t.Fatalf("语法错误语义改变: body=%q got=%v want=%v", body, got, want)
		}
		if validation := validateStrictJSONDocument([]byte(body), false); validation == nil || validation.Error() != want.Error() {
			t.Fatalf("只校验路径的语法错误改变: body=%q got=%v want=%v", body, validation, want)
		}
	}
}

// TestStrictJSONTokenBoundaries 覆盖数字、字面量、转义与无效 UTF-8 的标准库兼容边界。
func TestStrictJSONTokenBoundaries(t *testing.T) {
	for _, body := range []string{
		`{"numbers":[0,-0,1,-2,0.125,-0.5e-10,12E+99,1e10000]}`,
		`{"values":[true,false,null,"",[],{}],"escaped":"\"\\\/\b\f\n\r\t\u0000\uD800"}`,
		"{\"name\":\"\xff\",\"\xfe\":1}", " \t\r\n[1,2] \n", `"root"`, `null`,
	} {
		got, err := decodeStrictJSONValue([]byte(body))
		want, wantErr := referenceStrictJSONValue([]byte(body))
		if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("结构读取结果不一致: body=%q got=%#v err=%v want=%#v err=%v", body, got, err, want, wantErr)
		}
		if err := validateStrictJSONDocument([]byte(body), false); err != nil {
			t.Fatalf("只校验路径拒绝合法输入: %q: %v", body, err)
		}
	}
}
