package otlp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOTLPRejectsCaseInsensitiveHeaderCollisions 验证 JSON 与对象配置都拒绝 HTTP 语义重复字段。
func TestOTLPRejectsCaseInsensitiveHeaderCollisions(t *testing.T) {
	for _, names := range [][2]string{{"Authorization", "authorization"}, {"X-Tenant", "x-tenant"}, {"X-ID", "x-Id"}} {
		for _, sameValue := range []bool{false, true} {
			values := map[string]interface{}{names[0]: "synthetic-first", names[1]: "synthetic-second"}
			if sameValue {
				values[names[1]] = values[names[0]]
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []interface{}{values, string(encoded)} {
				config := enabledTelemetryValues()
				config["headers"] = raw
				for range 10 {
					if _, err := parseProviderConfig(config, "orders", "1", "test"); err == nil {
						t.Fatalf("大小写重复字段被接受: %v", names)
					} else if strings.Contains(err.Error(), "synthetic-") {
						t.Fatal("错误泄露了字段值")
					}
				}
			}
		}
	}
}
