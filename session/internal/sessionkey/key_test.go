package sessionkey

import (
	"regexp"
	"testing"
)

// TestValidCharacters 验证存储键字符约束，包括符号、控制字节与非 ASCII 输入。
func TestValidCharacters(t *testing.T) {
	allowed := regexp.MustCompile(`^[A-Za-z0-9_-]*$`)
	for value := range 256 {
		input := string([]byte{byte(value)})
		if got, want := ValidCharacters(input), allowed.MatchString(input); got != want {
			t.Errorf("字节 %#x 的存储键校验错误: %t", value, got)
		}
	}
	for _, input := range []string{"", "tenant_01-prod", "租户", "prefix-with:colon"} {
		if got, want := ValidCharacters(input), allowed.MatchString(input); got != want {
			t.Errorf("存储键 %q 的校验错误: %t", input, got)
		}
	}
}
