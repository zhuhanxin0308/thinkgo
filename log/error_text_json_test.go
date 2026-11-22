package log

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestErrorTextJSONEquivalentKeys(t *testing.T) {
	for _, policy := range []struct {
		name     string
		sanitize func(string) string
		prefix   string
	}{
		{"日志", SanitizeErrorText, "示例数据"},
		{"公开错误", SanitizeErrorTextFully, ""},
	} {
		t.Run(policy.name, func(t *testing.T) {
			for _, key := range []string{`"password"`, `"pass\u0077ord"`, `"user\u0050wd"`} {
				input := `[{"payload":{` + key + `:"\u793a例数据甲", "p\u0061ssport":"可见"}}]`
				output := policy.sanitize("记录 " + input + " 完成")
				var decoded []struct {
					Payload map[string]string
				}
				body := strings.TrimSuffix(strings.TrimPrefix(output, "记录 "), " 完成")
				if err := json.Unmarshal([]byte(body), &decoded); err != nil || len(decoded) != 1 {
					t.Fatalf("诊断片段不再是有效 JSON: %q (%v)", output, err)
				}
				for name, value := range decoded[0].Payload {
					want := policy.prefix + logRedactedPlaceholder
					if name == "passport" {
						want = "可见"
					}
					if value != want {
						t.Errorf("字段 %q 应为 %q，得到 %q", name, want, value)
					}
				}
				if len(decoded[0].Payload) != 2 || output != policy.sanitize(output) {
					t.Errorf("脱敏丢失字段或重复执行不稳定: %q", output)
				}
			}
		})
	}
}

func TestErrorTextJSONBoundaries(t *testing.T) {
	input := `记录 {"to\u006ben":1234,"status":1e999,"to\u006ben":{"nested":"仅示例"}} 及 {"a\u0075th":"短"} 完成`
	for _, sanitize := range []func(string) string{SanitizeErrorText, SanitizeErrorTextFully} {
		output := sanitize(input)
		if strings.Count(output, logRedactedPlaceholder) != 3 || strings.Contains(output, "仅示例") ||
			!strings.Contains(output, `"status":1e999`) || !strings.HasSuffix(output, " 完成") || sanitize(output) != output {
			t.Errorf("重复字段、非字符串值或片段边界处理错误: %q", output)
		}
	}
}

func TestErrorTextJSONUncertainBounds(t *testing.T) {
	const beginning = "前文 "
	for name, body := range map[string]string{
		"截断": `{"pass\u0077ord":"占位示例"`,
		"深度": `{"list":` + strings.Repeat("[", maxLogContextDepth+1) + `{"pass\u0077ord":"占位示例"}` + strings.Repeat("]", maxLogContextDepth+1) + "}",
		"长度": `{"ordinary":"` + strings.Repeat("x", maxLogContextBytes) + `","pass\u0077ord":"占位示例"}`,
		"节点": `{"list":[` + strings.Repeat("0,", maxLogContextNodes) + `0],"pass\u0077ord":"占位示例"}`,
	} {
		t.Run(name, func(t *testing.T) {
			for _, sanitize := range []func(string) string{SanitizeErrorText, SanitizeErrorTextFully} {
				output := sanitize(beginning + body)
				if !strings.HasPrefix(output, beginning) || len(output) > len(beginning)+len(logTruncatedPlaceholder) ||
					sanitize(output) != output {
					t.Errorf("无法确认的片段未被有界省略: 输出字节数 %d", len(output))
				}
			}
		})
	}
}

func TestErrorTextJSONLogAndFallback(t *testing.T) {
	const message = `诊断 {"pa\u0073sword":"演示尾", "status":200}`
	driver := newMockDriver()
	logger := NewLog(driver)
	t.Cleanup(func() { _ = logger.Close() })
	logger.Record(message, LevelWarning)
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("预期一条日志，得到 %d 条", len(entries))
	}
	failed := NewLog(&failingDriver{writeErr: errors.New(message)})
	var fallback bytes.Buffer
	failed.SetFallbackWriter(&fallback)
	failed.Write("触发兜底写入", LevelError)
	if err := failed.Close(); err == nil {
		t.Fatal("未返回驱动错误")
	}
	for _, output := range []string{entries[0].Message, entries[0].FormatEntry(), fallback.String()} {
		if strings.Contains(output, "演示尾") || !strings.Contains(output, logRedactedPlaceholder) ||
			!strings.Contains(output, `"status":200`) {
			t.Errorf("日志边界脱敏错误: %q", output)
		}
	}
}

func TestFreeTextBoundariesAroundJSON(t *testing.T) {
	for _, sanitize := range []func(string) string{SanitizeErrorText, SanitizeErrorTextFully} {
		const plain = "[[worker]] 状态 {ok} status=visible"
		if output := sanitize(plain); output != plain {
			t.Errorf("普通诊断标记发生变化: %q", output)
		}
		for _, first := range []string{"1", `"注释: [[draft]]"`, "true", "false", "null"} {
			input := "[" + first + `, "注释: [[draft]]", {"status":"ready"}]`
			if output := sanitize(input); output != input {
				t.Errorf("数组普通成员或诊断标签发生变化: %q", output)
			}
		}
		const compound = `password={"first":"一", "second":"二"} status=visible`
		if output := sanitize(compound); !strings.HasSuffix(output, " status=visible") ||
			strings.ContainsAny(output, "一二") || sanitize(output) != output {
			t.Errorf("位于明文凭据内的完整 JSON 值未被正确遮蔽: %q", output)
		}
		const multiline = "{\n\t\"status\":\"visible\",\n\"detail\":\"Authorization: Bearer 示例\"\n}"
		output := sanitize(multiline)
		if !json.Valid([]byte(output)) || !strings.Contains(output, "Bearer "+logRedactedPlaceholder) ||
			!strings.Contains(output, "visible") || strings.ContainsAny(output, "\r\n\t") || sanitize(output) != output {
			t.Errorf("格式化 JSON 内已有诊断文本未稳定脱敏: %q", output)
		}
	}
}
