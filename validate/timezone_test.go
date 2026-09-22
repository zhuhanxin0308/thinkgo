package validate

import (
	"testing"
	"time"
)

// TestValidatorDefaultsToUTC 验证日期解析和验证器空时区都固定回退 UTC。
func TestValidatorDefaultsToUTC(t *testing.T) {
	validator := NewValidator().SetLocation(nil)
	location, current := validator.runtimeTime(nil)
	if location != time.UTC || current.Location() != time.UTC {
		t.Fatalf("验证器默认时区必须为 UTC: location=%v current=%v", location, current.Location())
	}
	parsed, err := parseRuleTimeInLocation("2026-07-25 00:30:00", nil)
	if err != nil {
		t.Fatalf("按 UTC 解析日期失败: %v", err)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("无时区日期默认必须按 UTC 解析: %v", parsed.Location())
	}
}

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
