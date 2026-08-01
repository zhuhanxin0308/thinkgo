package framework

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewAppEnablesMetricsFromConfig 验证配置启用指标时应用初始化会打开注册表。
func TestNewAppEnablesMetricsFromConfig(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	configPath := filepath.Join(basePath, "config")
	appConfigPath := filepath.Join(configPath, "app.json")
	appConfig, err := os.ReadFile(appConfigPath)
	if err != nil {
		t.Fatalf("读取应用配置失败: %v", err)
	}
	appConfig = []byte(strings.Replace(string(appConfig), "{\n", "{\n  \"metrics_enable\": true,\n", 1))
	if err := os.WriteFile(appConfigPath, appConfig, 0o600); err != nil {
		t.Fatalf("写入指标配置失败: %v", err)
	}
	app := mustBuildTestApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })
	if app.metrics == nil || !app.metrics.Enabled() {
		t.Fatalf("配置启用后指标注册表未开启: %#v", app.metrics)
	}
	if err := app.log.Close(); err != nil {
		t.Fatalf("关闭测试日志失败: %v", err)
	}
}
