package framework

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/log"
)

// TestReadLogOverflowPolicy 验证日志溢出策略配置只接受明确的 sync/drop 值，并保持缺省同步语义。
func TestReadLogOverflowPolicy(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected log.OverflowPolicy
	}{
		{name: "missing", value: nil, expected: log.OverflowSync},
		{name: "sync", value: "sync", expected: log.OverflowSync},
		{name: "drop", value: "DROP", expected: log.OverflowDrop},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			policy, err := readLogOverflowPolicy(map[string]interface{}{"overflow_policy": testCase.value})
			if err != nil || policy != testCase.expected {
				t.Fatalf("日志溢出策略解析错误: policy=%d err=%v", policy, err)
			}
		})
	}
	for _, invalid := range []interface{}{"async", 1, true} {
		if _, err := readLogOverflowPolicy(map[string]interface{}{"overflow_policy": invalid}); err == nil {
			t.Fatalf("非法日志溢出策略应返回错误: %#v", invalid)
		}
	}
}

// TestCreateAppLogChannelRejectsInvalidOverflowPolicy 验证非法溢出策略在启动配置阶段不会静默回退。
func TestCreateAppLogChannelRejectsInvalidOverflowPolicy(t *testing.T) {
	app := &App{BasePath: t.TempDir()}
	logger, err := createAppLogChannel(app, map[string]interface{}{
		"type":            "file",
		"overflow_policy": "async",
	}, false)
	if logger != nil {
		_ = logger.Close()
	}
	if err == nil {
		t.Fatal("非法日志溢出策略必须阻断通道创建")
	}
	if !errors.Is(err, log.ErrInvalidOverflowPolicy) {
		t.Fatalf("非法日志溢出策略应保留明确错误链，实际为 %v", err)
	}
}
