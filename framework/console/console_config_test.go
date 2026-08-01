package console

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/config"
)

// TestConsoleAppliesConfiguredMetadata 验证控制台元数据会从应用配置服务加载。
func TestConsoleAppliesConfiguredMetadata(t *testing.T) {
	app := mustBuildConsoleConfigTestApp(t, config.NewConfig())
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		t.Fatalf("解析测试配置服务失败: %v", err)
	}
	if err := configuration.Set("console", map[string]interface{}{
		"name":      "订单控制台",
		"version":   "2.1.0",
		"user":      "deploy",
		"auto_path": "tools/generated",
	}); err != nil {
		t.Fatalf("写入控制台测试配置失败: %v", err)
	}

	cli := NewConsole(app)
	if cli.Name() != "订单控制台" || cli.Version() != "2.1.0" || cli.User() != "deploy" || cli.AutoPath() != "tools/generated" {
		t.Fatalf("控制台配置未进入运行时: name=%q version=%q user=%q auto_path=%q", cli.Name(), cli.Version(), cli.User(), cli.AutoPath())
	}
	stdout := &bytes.Buffer{}
	if err := cli.SetOutput(NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("设置测试输出失败: %v", err)
	}
	if err := cli.ShowHelp(); err != nil {
		t.Fatalf("展示配置后的帮助失败: %v", err)
	}
	for _, expected := range []string{"订单控制台 v2.1.0", "User: deploy"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("帮助输出缺少 %q: %q", expected, stdout.String())
		}
	}
}

// TestConsoleRejectsUnsafeConfiguredMetadata 验证控制台危险元数据会在执行前失败。
func TestConsoleRejectsUnsafeConfiguredMetadata(t *testing.T) {
	app := mustBuildConsoleConfigTestApp(t, config.NewConfig())
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		t.Fatalf("解析测试配置服务失败: %v", err)
	}
	if err := configuration.Set("console.name", "\x1b[31m危险名称"); err != nil {
		t.Fatalf("写入控制台名称配置失败: %v", err)
	}
	if err := configuration.Set("console.auto_path", "../outside"); err != nil {
		t.Fatalf("写入控制台路径配置失败: %v", err)
	}

	cli := NewConsole(app)
	if err := cli.Run(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("不安全 console 配置应返回 ErrInvalidConfig，实际为 %v", err)
	}
}

// mustBuildConsoleConfigTestApp 创建已初始化的控制台应用，确保服务解析处于明确生命周期状态。
func mustBuildConsoleConfigTestApp(t *testing.T, configuration *config.Config) *framework.App {
	t.Helper()
	basePath := t.TempDir()
	writeConsoleConfigTestFiles(t, basePath)
	app, err := framework.BuildConsoleApp(basePath)
	if err != nil {
		t.Fatalf("构建控制台测试应用失败: %v", err)
	}
	app.Instance(string(framework.ServiceConfig), configuration)
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// writeConsoleConfigTestFiles 为控制台配置测试准备严格构造所需的基础文件。
func writeConsoleConfigTestFiles(t *testing.T, basePath string) {
	t.Helper()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建控制台测试配置目录失败: %v", err)
	}
	configs := map[string]string{
		"app.json":     `{"app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`,
		"log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cache.json":   `{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`,
		"view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"cookie.json":  `{}`,
		"session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入控制台测试配置 %q 失败: %v", name, err)
		}
	}
}
