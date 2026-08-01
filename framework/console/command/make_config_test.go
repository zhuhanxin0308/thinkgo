package command

import (
	"os"
	"path/filepath"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/config"
)

// TestGeneratedSourceUsesConfiguredConsoleAutoPath 验证 console.auto_path 会改变生成器的实际输出目录。
func TestGeneratedSourceUsesConfiguredConsoleAutoPath(t *testing.T) {
	basePath := t.TempDir()
	app := buildConsoleTestApp(t, basePath)
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		t.Fatalf("解析测试配置服务失败: %v", err)
	}
	if err := configuration.Set("console.auto_path", "tools/generated"); err != nil {
		t.Fatalf("写入 console.auto_path 失败: %v", err)
	}

	if err := writeGeneratedAppSource(app, "controller", "user.go", []byte("package controller\n\ntype User struct{}\n")); err != nil {
		t.Fatalf("按 console.auto_path 生成源码失败: %v", err)
	}
	configuredPath := filepath.Join(basePath, "tools", "generated", "controller", "user.go")
	if _, err := os.Stat(configuredPath); err != nil {
		t.Fatalf("生成源码未落到配置目录 %q: %v", configuredPath, err)
	}

	if err := configuration.Set("console.auto_path", "../outside"); err != nil {
		t.Fatalf("写入非法 console.auto_path 失败: %v", err)
	}
	if _, err := applicationRelativeDirectory(app); err == nil {
		t.Fatal("console.auto_path 越出项目根目录时应被拒绝")
	}
}
