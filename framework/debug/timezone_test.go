package debug

import (
	"testing"
	"time"
)

// TestDebugUsesConfiguredLocation 验证调试面板中的时间使用应用配置的时区。
func TestDebugUsesConfiguredLocation(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	collector := NewRequestDebug(true)
	collector.SetLocation(location)
	collector.now = func() time.Time {
		return time.Date(2026, time.July, 24, 16, 30, 0, 0, time.UTC)
	}
	collector.AddLog("info", "timezone")

	info := collector.GetInfo()
	logs, ok := info["logs"].([]map[string]interface{})
	if !ok || len(logs) != 1 {
		t.Fatalf("调试日志记录结构错误: %#v", info["logs"])
	}
	if got := logs[0]["time"]; got != "00:30:00.000" {
		t.Fatalf("调试日志时间未使用应用时区: got=%#v", got)
	}
}
