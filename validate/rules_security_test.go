package validate

import (
	"math"
	"testing"
)

type validationPanickingStringer struct{}

func (validationPanickingStringer) String() string {
	panic("验证器不应调用 Stringer")
}

// TestValidatorRegexRuleSupportsQuotedAlternation 验证规则解析器不会把正则内部的管道误作规则分隔符。
func TestValidatorRegexRuleSupportsQuotedAlternation(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{
		"code": `required|regex:"^(foo|bar):\d+$"`,
	})
	for _, value := range []string{"foo:12", "bar:9"} {
		result, err := validator.Validate(map[string]interface{}{"code": value})
		if err != nil || !result.Valid() {
			t.Fatalf("值 %q 应通过带 alternation 的正则，result=%#v err=%v", value, result, err)
		}
	}
	result, err := validator.Validate(map[string]interface{}{"code": "baz:12"})
	if err != nil {
		t.Fatalf("执行正则验证失败: %v", err)
	}
	if result.Valid() {
		t.Fatal("不匹配正则的值应验证失败")
	}
	slashDelimited := NewValidator().SetRules(map[string]string{"code": `regex:/^(foo|bar)$/`})
	result, err = slashDelimited.Validate(map[string]interface{}{"code": "bar"})
	if err != nil || !result.Valid() {
		t.Fatalf("斜杠分隔正则中的管道不应拆分规则，result=%#v err=%v", result, err)
	}
}

// TestValidatorAliasAndMessageInterpolation 验证字段别名进入用户消息，实际字段名仍可用于定位。
func TestValidatorAliasAndMessageInterpolation(t *testing.T) {
	validator := NewValidator().
		SetRules(map[string]string{"password|密码": "length:8,32"}).
		SetMessages(map[string]string{"password.length": "{:field}长度必须为{:param}，规则={:rule}"})
	result, err := validator.Validate(map[string]interface{}{"password": "短"})
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	violations := result.Violations()
	if len(violations) != 1 {
		t.Fatalf("应返回一条错误，实际为 %#v", violations)
	}
	violation := violations[0]
	if violation.Field != "password" || violation.Alias != "密码" || violation.Rule != "length" {
		t.Fatalf("错误定位信息不完整: %#v", violation)
	}
	if violation.Message != "密码长度必须为8,32，规则=length" {
		t.Fatalf("自定义消息变量替换错误: %q", violation.Message)
	}
}

// TestValidatorUnicodeLengthAndTypedCollections 验证长度按 Unicode 字符计数且集合规则支持类型化容器。
func TestValidatorUnicodeLengthAndTypedCollections(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{
		"name":    "length:2",
		"tags":    "required|array|length:2",
		"mapping": "required|array|length:1",
	})
	valid, err := validator.Validate(map[string]interface{}{
		"name":    "你好",
		"tags":    []string{"go", "web"},
		"mapping": map[string]int{"answer": 42},
	}, CollectAllErrors())
	if err != nil || !valid.Valid() {
		t.Fatalf("Unicode 和类型化集合应通过验证，result=%#v err=%v", valid, err)
	}

	invalid, err := validator.Validate(map[string]interface{}{
		"name":    "你",
		"tags":    []string{},
		"mapping": map[string]int{},
	}, CollectAllErrors())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if len(invalid.Violations()) != 5 {
		t.Fatalf("批量模式应报告空集合的 required/length 及 Unicode 长度错误，实际为 %#v", invalid.Violations())
	}
}

// TestValidatorLengthRulesRejectNonSizedScalars 验证布尔值和数值不会被格式化后冒充字符串长度。
func TestValidatorLengthRulesRejectNonSizedScalars(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{
		"number":  "length:1",
		"boolean": "max:4",
	})
	result, err := validator.Validate(map[string]interface{}{
		"number":  7,
		"boolean": true,
	}, CollectAllErrors())
	if err != nil {
		t.Fatalf("执行长度类型验证失败: %v", err)
	}
	if len(result.Violations()) != 2 {
		t.Fatalf("非字符串标量不能按格式化文本计算长度，实际为 %#v", result.Violations())
	}
}

// TestValidatorNumericRulesRejectNonFiniteAndPreserveIntegerPrecision 验证数值规则拒绝 NaN/Inf 并避免 float64 精度丢失。
func TestValidatorNumericRulesRejectNonFiniteAndPreserveIntegerPrecision(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{
		"number": "number",
		"limit":  "integer|gt:18446744073709551614",
	})
	for _, nonFinite := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		result, err := validator.Validate(map[string]interface{}{
			"number": nonFinite,
			"limit":  uint64(math.MaxUint64),
		})
		if err != nil {
			t.Fatalf("执行数值验证失败: %v", err)
		}
		if result.Valid() || result.Violations()[0].Field != "number" {
			t.Fatalf("非有限数必须失败，实际为 %#v", result.Violations())
		}
	}
	for _, nonDecimal := range []string{"1/2", "0x10"} {
		result, err := validator.Validate(map[string]interface{}{
			"number": nonDecimal,
			"limit":  uint64(math.MaxUint64),
		})
		if err != nil || result.Valid() {
			t.Fatalf("非十进制表示 %q 必须失败，result=%#v err=%v", nonDecimal, result, err)
		}
	}
	result, err := validator.Validate(map[string]interface{}{
		"number": 1,
		"limit":  uint64(math.MaxUint64),
	})
	if err != nil || !result.Valid() {
		t.Fatalf("uint64 最大值应精确大于边界，result=%#v err=%v", result, err)
	}
}

// TestValidatorEmailURLAndStringerSafety 验证现代邮箱、完整 URL 以及非标量 Stringer 的安全边界。
func TestValidatorEmailURLAndStringerSafety(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{
		"email": "email",
		"url":   "url",
		"alpha": "alpha",
	})
	valid, err := validator.Validate(map[string]interface{}{
		"email": "user@example.technology",
		"url":   "https://example.com/path?q=1",
		"alpha": "ThinkGo",
	}, CollectAllErrors())
	if err != nil || !valid.Valid() {
		t.Fatalf("合法邮箱和 URL 应通过，result=%#v err=%v", valid, err)
	}

	invalid, err := validator.Validate(map[string]interface{}{
		"email": "user..name@example.com",
		"url":   "https://example.com/path\nforged",
		"alpha": validationPanickingStringer{},
	}, CollectAllErrors())
	if err != nil {
		t.Fatalf("执行安全边界验证失败: %v", err)
	}
	if len(invalid.Violations()) != 3 {
		t.Fatalf("三个非法值都应失败，实际为 %#v", invalid.Violations())
	}
}

// TestValidatorConfirmRequiresTargetAndMatchingType 验证确认字段必须存在且类型和值均一致。
func TestValidatorConfirmRequiresTargetAndMatchingType(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{"code": "confirm:code_confirm"})
	invalidCases := []map[string]interface{}{
		{"code": "1"},
		{"code": "1", "code_confirm": 1},
		{"code": nil},
	}
	for _, data := range invalidCases {
		result, err := validator.Validate(data)
		if err != nil {
			t.Fatalf("执行 confirm 验证失败: %v", err)
		}
		if result.Valid() {
			t.Fatalf("确认字段缺失或类型不同应失败: %#v", data)
		}
	}
	result, err := validator.Validate(map[string]interface{}{"code": "1", "code_confirm": "1"})
	if err != nil || !result.Valid() {
		t.Fatalf("类型和值一致的确认字段应通过，result=%#v err=%v", result, err)
	}
}

// TestValidatorIDCardRejectsImpossibleBirthDate 验证身份证校验包含出生日期和顺序码语义。
func TestValidatorIDCardRejectsImpossibleBirthDate(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{"id": "idCard"})
	invalidID := testIDCardWithChecksum("11010520230230002")
	result, err := validator.Validate(map[string]interface{}{"id": invalidID})
	if err != nil {
		t.Fatalf("执行身份证验证失败: %v", err)
	}
	if result.Valid() {
		t.Fatalf("校验码正确但出生日期非法的身份证应失败: %s", invalidID)
	}
}

func testIDCardWithChecksum(firstSeventeen string) string {
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	codes := [...]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
	sum := 0
	for index := range weights {
		sum += int(firstSeventeen[index]-'0') * weights[index]
	}
	return firstSeventeen + string(codes[sum%11])
}
