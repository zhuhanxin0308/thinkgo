package framework

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/debug"
)

// TestCreateAppTraceMatchesThinkPHPConfig 验证 Html 默认值和 Console 类型均来自 trace 配置。
func TestCreateAppTraceMatchesThinkPHPConfig(t *testing.T) {
	manager := debug.NewDebug()
	defaultTrace, err := createAppTrace(map[string]interface{}{}, manager, nil)
	if err != nil || defaultTrace.Type != "Html" || defaultTrace.Channel != "" || defaultTrace.Debug != manager {
		t.Fatalf("默认 Trace 配置错误: trace=%#v err=%v", defaultTrace, err)
	}
	consoleTrace, err := createAppTrace(map[string]interface{}{"type": "Console", "channel": "file"}, manager, nil)
	if err != nil || consoleTrace.Type != "Console" || consoleTrace.Channel != "file" {
		t.Fatalf("Console Trace 配置错误: trace=%#v err=%v", consoleTrace, err)
	}
}

// TestCreateAppTraceRejectsInvalidBuiltins 验证非法类型和控制字符不会静默回退。
func TestCreateAppTraceRejectsInvalidBuiltins(t *testing.T) {
	for _, configuration := range []map[string]interface{}{
		{"type": "Unknown"},
		{"type": 1},
		{"channel": "file\nnext"},
	} {
		if trace, err := createAppTrace(configuration, debug.NewDebug(), nil); err == nil || trace != nil {
			t.Fatalf("非法 Trace 配置应失败: config=%#v trace=%#v err=%v", configuration, trace, err)
		}
	}
}
