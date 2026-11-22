package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestEnvironmentJSONRejectsDuplicateMembersBeforeCommit 验证所有集合入口在解码丢失重复键之前拒绝整个覆盖批次。
func TestEnvironmentJSONRejectsDuplicateMembersBeforeCommit(t *testing.T) {
	tests := []struct {
		name      string
		prototype interface{}
		raw       string
	}{
		{name: "对象重复成员", prototype: map[string]interface{}{}, raw: `{"tls":"true","tls":"false"}`},
		{name: "嵌套对象重复成员", prototype: map[string]interface{}{}, raw: `{"params":{"tls":"true","tls":"false"}}`},
		{name: "转义后的相同成员", prototype: map[string]interface{}{}, raw: `{"tls":"true","\u0074ls":"false"}`},
		{name: "对象大小写冲突", prototype: map[string]interface{}{}, raw: `{"TLS":"true","tls":"false"}`},
		{name: "数组中的对象", prototype: []interface{}{}, raw: `[{"tls":"true","tls":"false"}]`},
		{name: "类型化对象", prototype: map[string]string{}, raw: `{"tls":"true","tls":"false"}`},
		{name: "类型化对象大小写冲突", prototype: map[string]string{}, raw: `{"TLS":"true","tls":"false"}`},
		{name: "类型化嵌套对象", prototype: map[string]map[string]string{}, raw: `{"params":{"tls":"true","tls":"false"}}`},
		{name: "类型化切片", prototype: []map[string]string{}, raw: `[{"tls":"true","tls":"false"}]`},
		{name: "定长数组", prototype: [1]map[string]string{}, raw: `[{"tls":"true","tls":"false"}]`},
		{name: "键和值均需脱敏", prototype: map[string]interface{}{}, raw: `{"private-key-token":"private-value-token","private-key-token":"replacement"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := NewConfig()
			if err := configuration.Set("app.debug", false); err != nil {
				t.Fatal(err)
			}
			if err := configuration.Set("database.params", test.prototype); err != nil {
				t.Fatal(err)
			}
			before := configuration.GetMap("")
			err := configuration.ApplyEnvironment(environmentStub{
				"APP_DEBUG":       "true",
				"DATABASE_PARAMS": test.raw,
			})
			if !errors.Is(err, ErrConfigEnvironmentType) {
				t.Fatalf("重复 JSON 成员应返回可检查的配置类型错误，实际为 %v", err)
			}
			var valueErr *ConfigEnvironmentValueError
			if !errors.As(err, &valueErr) || valueErr.EnvironmentName != "DATABASE_PARAMS" || valueErr.Path != "database.params" {
				t.Fatalf("重复成员错误应保留环境变量和配置路径上下文: %v", err)
			}
			for _, secret := range []string{test.raw, "private-key-token", "private-value-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("错误泄漏环境变量内容: %v", err)
				}
			}
			if after := configuration.GetMap(""); !reflect.DeepEqual(before, after) {
				t.Fatalf("失败批次不应提交任何已解析的覆盖: before=%#v after=%#v", before, after)
			}
			if _, fileErr := decodeConfigJSON([]byte(`{"params":` + test.raw + `}`)); !errors.Is(fileErr, ErrConfigDuplicateKey) {
				t.Fatalf("文件和环境变量应拒绝同一重复成员: %v", fileErr)
			}
		})
	}
}

// TestEnvironmentJSONKeepsIndependentObjectsAndExactNumbers 验证不同对象可复用成员名，数字与类型化集合保持原语义。
func TestEnvironmentJSONKeepsIndependentObjectsAndExactNumbers(t *testing.T) {
	tests := []struct {
		name      string
		prototype interface{}
		raw       string
		want      interface{}
	}{
		{
			name:      "独立对象的相同成员",
			prototype: []interface{}{},
			raw:       `[{"ID":9007199254740993},{"ID":18446744073709551615}]`,
			want: []interface{}{
				map[string]interface{}{"id": json.Number("9007199254740993")},
				map[string]interface{}{"id": json.Number("18446744073709551615")},
			},
		},
		{
			name:      "超出浮点范围的合法数字",
			prototype: map[string]interface{}{},
			raw:       `{"value":1e309}`,
			want:      map[string]interface{}{"value": json.Number("1e309")},
		},
		{
			name:      "类型化切片保持类型",
			prototype: []map[string]string{},
			raw:       `[{"Name":"first"},{"Name":"second"}]`,
			want:      []map[string]string{{"Name": "first"}, {"Name": "second"}},
		},
		{
			name:      "定长数组保持类型",
			prototype: [1]map[string]string{},
			raw:       `[{"Name":"first"}]`,
			want:      [1]map[string]string{{"Name": "first"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := NewConfig()
			if err := configuration.Set("feature.values", test.prototype); err != nil {
				t.Fatal(err)
			}
			if err := configuration.ApplyEnvironment(environmentStub{"FEATURE_VALUES": test.raw}); err != nil {
				t.Fatalf("无歧义集合应成功覆盖: %v", err)
			}
			if got := configuration.Get("feature.values"); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("合法集合类型或值发生变化: want=%#v got=%#v", test.want, got)
			}
		})
	}
}

// TestEnvironmentJSONBoundsTypedCollectionDepth 验证强类型集合也在解码前遵守统一的嵌套深度限制。
func TestEnvironmentJSONBoundsTypedCollectionDepth(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("feature.values", []map[string]interface{}{}); err != nil {
		t.Fatal(err)
	}
	raw := `[{"value":` + strings.Repeat("[", maxConfigJSONDepth) + "0" + strings.Repeat("]", maxConfigJSONDepth) + `}]`
	err := configuration.ApplyEnvironment(environmentStub{"FEATURE_VALUES": raw})
	if !errors.Is(err, ErrConfigEnvironmentType) {
		t.Fatalf("过深的 JSON 应返回脱敏的配置类型错误: %v", err)
	}
	if got := configuration.Get("feature.values").([]map[string]interface{}); len(got) != 0 {
		t.Fatal("过深 JSON 不得改变默认集合")
	}
}
