package validate

import "testing"

// TestValidatorRuleMatrix 验证每条公开规则的通过与失败路径。
func TestValidatorRuleMatrix(t *testing.T) {
	testCases := []struct {
		name         string
		rule         string
		validValue   interface{}
		invalidValue interface{}
		validExtra   map[string]interface{}
		invalidExtra map[string]interface{}
	}{
		{name: "required", rule: "required", validValue: 0, invalidValue: "  "},
		{name: "number", rule: "number", validValue: "-1.25e2", invalidValue: "1x"},
		{name: "integer", rule: "integer", validValue: float64(12), invalidValue: "1.2"},
		{name: "float", rule: "float", validValue: 1.25, invalidValue: true},
		{name: "boolean", rule: "boolean", validValue: "TRUE", invalidValue: "yes"},
		{name: "email", rule: "email", validValue: "user@example.com", invalidValue: "user@"},
		{name: "array", rule: "array", validValue: [2]int{1, 2}, invalidValue: "array"},
		{name: "accepted", rule: "accepted", validValue: true, invalidValue: false},
		{name: "date", rule: "date", validValue: "2024-02-29", invalidValue: "2023-02-29"},
		{name: "alpha", rule: "alpha", validValue: "Go", invalidValue: "Go1"},
		{name: "alphaNum", rule: "alphaNum", validValue: "Go1", invalidValue: "Go-1"},
		{name: "alphaDash", rule: "alphaDash", validValue: "Go_1-x", invalidValue: "Go x"},
		{name: "chs", rule: "chs", validValue: "中文", invalidValue: "中文A"},
		{name: "chsAlpha", rule: "chsAlpha", validValue: "中文Go", invalidValue: "中文1"},
		{name: "chsAlphaNum", rule: "chsAlphaNum", validValue: "中文Go1", invalidValue: "中文-"},
		{name: "chsDash", rule: "chsDash", validValue: "中文_Go-1", invalidValue: "中文 Go"},
		{name: "ip", rule: "ip", validValue: "127.0.0.1", invalidValue: "2001:db8::1"},
		{name: "url", rule: "url", validValue: "ftp://example.com/file", invalidValue: "javascript:alert(1)"},
		{name: "in", rule: "in:red,green", validValue: "green", invalidValue: "blue"},
		{name: "notIn", rule: "notIn:red,green", validValue: "blue", invalidValue: "red"},
		{name: "between", rule: "between:1,10", validValue: 10, invalidValue: 11},
		{name: "notBetween", rule: "notBetween:1,10", validValue: 11, invalidValue: 10},
		{name: "length", rule: "length:2,4", validValue: "中文", invalidValue: "中"},
		{name: "maxNumeric", rule: "max:10", validValue: 10, invalidValue: 11},
		{name: "maxLength", rule: "max:2", validValue: "中文", invalidValue: "中文长"},
		{name: "minNumeric", rule: "min:2", validValue: 2, invalidValue: 1},
		{name: "minLength", rule: "min:2", validValue: "中文", invalidValue: "中"},
		{name: "eq", rule: "eq:active", validValue: "active", invalidValue: "inactive"},
		{name: "gt", rule: "gt:10", validValue: 11, invalidValue: 10},
		{name: "lt", rule: "lt:10", validValue: 9, invalidValue: 10},
		{name: "egt", rule: "egt:10", validValue: 10, invalidValue: 9},
		{name: "elt", rule: "elt:10", validValue: 10, invalidValue: 11},
		{name: "regex", rule: `regex:"^[A-Z]{2}$"`, validValue: "GO", invalidValue: "Go"},
		{name: "confirm", rule: "confirm:other", validValue: "same", invalidValue: "first", validExtra: map[string]interface{}{"other": "same"}, invalidExtra: map[string]interface{}{"other": "second"}},
		{name: "different", rule: "different:other", validValue: "first", invalidValue: "same", validExtra: map[string]interface{}{"other": "second"}, invalidExtra: map[string]interface{}{"other": "same"}},
		{name: "mobile", rule: "mobile", validValue: "13812345678", invalidValue: "12812345678"},
		{name: "dateFormat", rule: "dateFormat:2006/01/02", validValue: "2024/01/02", invalidValue: "2024-01-02"},
		{name: "afterLiteral", rule: "after:2024-01-01", validValue: "2024-01-02", invalidValue: "2024-01-01"},
		{name: "beforeField", rule: "before:other", validValue: "2024-01-01", invalidValue: "2024-01-03", validExtra: map[string]interface{}{"other": "2024-01-02"}, invalidExtra: map[string]interface{}{"other": "2024-01-02"}},
		{name: "requireIf", rule: "requireIf:type,staff", validValue: "company", invalidValue: "", validExtra: map[string]interface{}{"type": "staff"}, invalidExtra: map[string]interface{}{"type": "staff"}},
		{name: "requireWith", rule: "requireWith:other", validValue: "present", invalidValue: "", validExtra: map[string]interface{}{"other": "set"}, invalidExtra: map[string]interface{}{"other": "set"}},
		{name: "idCard", rule: "idCard", validValue: "11010519491231002X", invalidValue: "110105194912310021"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			validator := NewValidator().SetRules(map[string]string{"value": testCase.rule})
			validData := map[string]interface{}{"value": testCase.validValue}
			for key, value := range testCase.validExtra {
				validData[key] = value
			}
			validResult, err := validator.Validate(validData)
			if err != nil || !validResult.Valid() {
				t.Fatalf("合法值应通过，result=%#v err=%v", validResult, err)
			}

			invalidData := map[string]interface{}{"value": testCase.invalidValue}
			for key, value := range testCase.invalidExtra {
				invalidData[key] = value
			}
			invalidResult, err := validator.Validate(invalidData)
			if err != nil {
				t.Fatalf("执行非法值验证失败: %v", err)
			}
			if invalidResult.Valid() {
				t.Fatalf("非法值应失败: %#v", invalidData)
			}
		})
	}
}

// TestOptionalAndConditionalRulesSkipWhenInactive 验证缺失的非必填字段和未触发条件不会误报。
func TestOptionalAndConditionalRulesSkipWhenInactive(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{
		"optional_email": "email",
		"company":        "requireIf:type,staff",
		"attachment":     "requireWith:path",
	})
	result, err := validator.Validate(map[string]interface{}{"type": "guest"}, CollectAllErrors())
	if err != nil || !result.Valid() {
		t.Fatalf("未触发的可选规则应通过，result=%#v err=%v", result, err)
	}
}
