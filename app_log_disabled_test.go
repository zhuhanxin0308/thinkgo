package framework

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

// TestLogCloseConfigurationDisablesAllLevels 验证关闭配置独立于全局级别，且后续级别设置不能重新启用日志。
func TestLogCloseConfigurationDisablesAllLevels(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		levels string
	}{
		{name: "默认级别", levels: "null"},
		{name: "空级别", levels: "[]"},
		{name: "错误级别", levels: `["error"]`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			configuration := `{"close":true,"level":` + testCase.levels + `,"default":"file","channels":{"file":{"type":"file","path":"runtime/closed-log"}}}`
			if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(configuration), 0o600); err != nil {
				t.Fatalf("写入日志关闭配置失败: %v", err)
			}
			app := NewConsoleAppUninitialized(basePath)
			t.Cleanup(func() {
				if err := app.Close(); err != nil {
					t.Errorf("关闭日志配置测试应用失败: %v", err)
				}
			})
			original := app.Log()
			if err := app.Initialize(); err != nil {
				t.Fatalf("有效关闭配置不应阻断应用初始化: %v", err)
			}
			logger, err := ResolveServiceAs[*log.Log](app, ServiceLog)
			if err != nil || logger == nil || logger != app.Log() || logger == original {
				t.Fatalf("关闭日志 Provider 未同步替换实例: original=%p resolved=%p facade=%p err=%v", original, logger, app.Log(), err)
			}
			assertApplicationLogDisabled(t, logger)
			for _, levels := range [][]string{nil, {log.LevelError, log.LevelInfo}} {
				logger.SetLevels(levels)
				assertApplicationLogDisabled(t, logger)
			}
			logger.InfoCtx("关闭配置回归", map[string]any{"password": "private", "order_id": 42})
			if err := logger.Flush(context.Background()); err != nil {
				t.Fatalf("关闭配置下 Flush 应正常结束: %v", err)
			}
			if err := original.Flush(context.Background()); !errors.Is(err, log.ErrLogClosed) {
				t.Fatalf("Provider 应关闭被替换的初始日志器，实际为 %v", err)
			}
			if _, err := os.Stat(filepath.Join(basePath, "runtime", "closed-log")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("关闭日志不应创建文件驱动目录，实际为 %v", err)
			}
		})
	}
}

func assertApplicationLogDisabled(t *testing.T, logger *log.Log) {
	t.Helper()
	for _, level := range []string{
		log.LevelEmergency, log.LevelAlert, log.LevelCritical, log.LevelError,
		log.LevelWarning, log.LevelNotice, log.LevelInfo, log.LevelDebug, log.LevelSQL,
		"custom", " ERROR ",
	} {
		if logger.IsLevelEnabled(level) {
			t.Errorf("log.close=true 不应开启级别 %q", level)
		}
	}
}

// TestLogCloseConfigurationKeepsStrictValidation 验证禁用输出仍保留顶层配置的启动错误，不静默接受错误配置。
func TestLogCloseConfigurationKeepsStrictValidation(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		configuration string
		message       string
	}{
		{name: "未知字段", configuration: `{"close":true,"channels":{},"unexpected":true}`, message: "未知字段"},
		{name: "错误级别类型", configuration: `{"close":true,"channels":{},"level":"error"}`, message: "level"},
		{name: "错误通道容器", configuration: `{"close":true,"channels":[]}`, message: "channels"},
		{name: "错误关闭类型", configuration: `{"close":"true","channels":{}}`, message: "close"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(testCase.configuration), 0o600); err != nil {
				t.Fatalf("写入无效日志配置失败: %v", err)
			}
			_, err := initializeTestConsoleApp(t, basePath)
			if err == nil || !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("无效关闭配置必须保留明确启动错误 %q，实际为 %v", testCase.message, err)
			}
		})
	}
}

type lifecycleLogCountingError struct {
	calls int
}

func (failure *lifecycleLogCountingError) Error() string {
	failure.calls++
	return "生命周期测试错误"
}

// TestLifecycleLogFiltersBeforeErrorEvaluation 验证生命周期日志在错误转换前过滤，开启时继续报告。
func TestLifecycleLogFiltersBeforeErrorEvaluation(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		level     string
		wantCalls int
	}{
		{name: "过滤错误", level: log.LevelInfo, wantCalls: 0},
		{name: "开启错误", level: log.LevelError, wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			logger := log.NewLog()
			logger.SetLevels([]string{testCase.level})
			t.Cleanup(func() { _ = logger.Close() })
			app := &App{log: logger}
			failure := &lifecycleLogCountingError{}
			app.logLifecycleError("生命周期失败", failure)
			if failure.calls != testCase.wantCalls {
				t.Fatalf("错误求值次数为 %d，期望 %d", failure.calls, testCase.wantCalls)
			}
		})
	}
}
