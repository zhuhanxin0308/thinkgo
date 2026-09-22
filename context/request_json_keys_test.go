package context

import (
	"fmt"
	"strings"
	"testing"
)

// TestStrictJSONValidationKeyStorageBoundaries 验证小对象、扩展字典和标准解码等价键都拒绝重复。
func TestStrictJSONValidationKeyStorageBoundaries(t *testing.T) {
	for _, count := range []int{0, 1, 7, 8, 9, 32, 128} {
		fields := make([]string, count)
		for index := range fields {
			fields[index] = fmt.Sprintf(`"field%d":%d`, index, index)
		}
		for _, pair := range []string{
			`"name":1,"\u006eame":2`, `"\u006eame":1,"name":2`, `"":1,"":2`,
			`"中文":1,"\u4e2d文":2`, `"\uD800":1,"\ufffd":2`, "\"\xff\":1,\"\\ufffd\":2",
		} {
			members := strings.Join(fields, ",")
			if members != "" {
				members += ","
			}
			body := []byte("{" + members + pair + "}")
			if err := validateStrictJSONDocument(body, true); err == nil {
				t.Fatalf("字段数 %d 未拒绝解码后重名: %q", count, body)
			}
		}
		if err := validateStrictJSONDocument([]byte("{"+strings.Join(fields, ",")+"}"), true); err != nil {
			t.Fatalf("字段数 %d 错误拒绝唯一键: %v", count, err)
		}
		if count > 0 {
			body := []byte("{" + strings.Join(fields, ",") + `,"field0":999}`)
			if err := validateStrictJSONDocument(body, true); err == nil {
				t.Fatalf("字段数 %d 的字典扩展丢失了早期字段", count)
			}
		}
	}
}
