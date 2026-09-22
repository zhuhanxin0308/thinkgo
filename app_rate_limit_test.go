package framework

import (
	"encoding/json"
	"testing"
)

// TestCreateAppRateLimitStrictConfiguration 验证默认关闭、启用和所有非法配置都在启动期失败。
func TestCreateAppRateLimitStrictConfiguration(t *testing.T) {
	if handler, enabled, err := createAppRateLimit(map[string]interface{}{"enable": false}); err != nil || enabled || handler != nil {
		t.Fatalf("默认关闭限流结果错误: enabled=%t handler=%v err=%v", enabled, handler, err)
	}
	handler, enabled, err := createAppRateLimit(map[string]interface{}{
		"enable":         true,
		"rate":           json.Number("10"),
		"period_seconds": json.Number("1"),
		"burst":          json.Number("20"),
		"max_keys":       json.Number("1000"),
	})
	if err != nil || !enabled || handler == nil {
		t.Fatalf("启用限流失败: enabled=%t handler=%v err=%v", enabled, handler, err)
	}
	invalid := []map[string]interface{}{
		{"enable": true, "unknown": true},
		{"enable": "true"},
		{"enable": true, "rate": json.Number("0")},
		{"enable": true, "period_seconds": json.Number("31536001")},
		{"enable": true, "max_keys": json.Number("10000001")},
	}
	for _, values := range invalid {
		if _, _, err := createAppRateLimit(values); err == nil {
			t.Fatalf("非法限流配置必须被拒绝: %#v", values)
		}
	}
}
