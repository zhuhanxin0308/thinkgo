package command

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactConfigDumpDataMasksNestedSensitiveValues 验证配置导出前会递归脱敏敏感字段。
func TestRedactConfigDumpDataMasksNestedSensitiveValues(t *testing.T) {
	source := map[string]interface{}{
		"database": map[string]interface{}{
			"username": "root",
			"password": "db-secret",
			"params": []interface{}{
				map[string]interface{}{"api_token": "token-secret"},
				"safe-value",
			},
		},
		"Authorization": "Bearer secret",
		"normal":        "visible",
	}

	redacted := redactConfigDumpData(source)
	jsonBytes, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("脱敏后的配置应可序列化: %v", err)
	}
	output := string(jsonBytes)

	for _, secret := range []string{"db-secret", "token-secret", "Bearer secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("配置导出不应包含敏感值 %q，实际为 %s", secret, output)
		}
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("配置导出应包含脱敏占位符，实际为 %s", output)
	}
	if !strings.Contains(output, "visible") || !strings.Contains(output, "root") {
		t.Fatalf("配置导出应保留非敏感字段，实际为 %s", output)
	}
	if source["Authorization"] != "Bearer secret" {
		t.Fatalf("脱敏过程不应修改原始配置，实际为 %#v", source)
	}
}
