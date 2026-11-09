package lang

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLanguageInterpolationDoesNotExpandValues 验证变量只替换原模板，插入文本保持原样。
func TestLanguageInterpolationDoesNotExpandValues(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		variables map[string]interface{}
		want      string
	}{
		{"后续变量", "A={:first}; B={:second}", map[string]interface{}{"first": "{:second}", "second": "completed"}, "A={:second}; B=completed"},
		{"只在值中出现", "{:first}", map[string]interface{}{"first": "{:second}", "second": "completed"}, "{:second}"},
		{"反向变量", "{:second}|{:first}", map[string]interface{}{"second": "{:first}", "first": "ok"}, "{:first}|ok"},
		{"自引用与重复", "{:value}/{:value}", map[string]interface{}{"value": "{:value}"}, "{:value}/{:value}"},
		{"互相引用", "{:a}|{:b}", map[string]interface{}{"a": "{:b}", "b": "{:a}"}, "{:b}|{:a}"},
		{"大小写与缺失", "{:Name}|{:name}|{:missing}", map[string]interface{}{"Name": "大写", "name": "小写"}, "大写|小写|{:missing}"},
		{"非法对象", "{:unsafe}|{:count}|{:ok}", map[string]interface{}{"unsafe": panickingLangValue{}, "count": 42, "ok": true}, "{:unsafe}|42|true"},
		{"非法变量名", "{:bad@name}|{:ok}", map[string]interface{}{"bad@name": "替换", "ok": "值"}, "{:bad@name}|值"},
		{"空值", "a{:empty}b", map[string]interface{}{"empty": ""}, "ab"},
		{"无变量", "{:missing}", nil, "{:missing}"},
		{"无有效变量", "{:value}", map[string]interface{}{"value": panickingLangValue{}}, "{:value}"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			content, err := json.Marshal(map[string]string{"message": testCase.template})
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "zh-cn.json")
			if err := os.WriteFile(file, content, 0o600); err != nil {
				t.Fatal(err)
			}
			manager := NewLang()
			if err := manager.Load(file, "zh-cn"); err != nil {
				t.Fatal(err)
			}
			if got := manager.Get("message", testCase.variables, "zh-cn"); got != testCase.want {
				t.Fatalf("翻译插值=%q，预期=%q", got, testCase.want)
			}
		})
	}
}
