package validate

import (
	"testing"
	"time"
)

// TestValidatorUsesConfiguredLocationForDateRules 验证身份证日期边界使用应用时区而非进程时区。
func TestValidatorUsesConfiguredLocationForDateRules(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	validator := NewValidator().SetRules(map[string]string{"id": "idCard"})
	validator.now = func() time.Time {
		return time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	}
	result, err := validator.Validate(
		map[string]interface{}{"id": buildTestIDCard("110105", "20260725", "001")},
		WithLocation(location),
	)
	if err != nil {
		t.Fatalf("按应用时区执行验证失败: %v", err)
	}
	if !result.Valid() {
		t.Fatalf("应用时区当天出生日期不应被判定为未来日期: %#v", result.Violations())
	}
}

func buildTestIDCard(area, birthday, sequence string) string {
	base := area + birthday + sequence
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	codes := [...]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
	sum := 0
	for index, weight := range weights {
		sum += int(base[index]-'0') * weight
	}
	return base + string(codes[sum%11])
}
