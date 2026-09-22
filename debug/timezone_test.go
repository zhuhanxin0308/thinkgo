package debug

import (
	"testing"
	"time"
)

// TestDebugDefaultsToUTC 验证调试信息默认时间不依赖部署机器时区。
func TestDebugDefaultsToUTC(t *testing.T) {
	collector := NewRequestDebug(true)
	if collector.location != time.UTC || collector.start.Location() != time.UTC {
		t.Fatalf("调试实例默认时区必须为 UTC: location=%v start=%v", collector.location, collector.start.Location())
	}
	collector.SetLocation(nil)
	if collector.location != time.UTC {
		t.Fatalf("nil 调试时区必须回退 UTC: %v", collector.location)
	}
}

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
