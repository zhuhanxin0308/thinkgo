package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
)

// TestGeneratedSourceKeepsNativeApplicationPath 验证业务生成器始终遵循
// app/<应用名> 目录，控制台自身的 auto_path 不会改变业务架构。
func TestGeneratedSourceKeepsNativeApplicationPath(t *testing.T) {
	basePath := t.TempDir()
	app := buildConsoleTestApp(t, basePath)
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		t.Fatalf("解析测试配置服务失败: %v", err)
	}
	if err := configuration.Set("console.auto_path", "tools/generated"); err != nil {
		t.Fatalf("写入 console.auto_path 失败: %v", err)
	}

	if err := writeGeneratedAppSource(app, generatorTarget{application: "index", name: "User"}, "controller", "user.go", []byte("package controller\n\ntype User struct{}\n")); err != nil {
		t.Fatalf("按 console.auto_path 生成源码失败: %v", err)
	}
	configuredPath := filepath.Join(basePath, "app", "index", "controller", "user.go")
	if _, err := os.Stat(configuredPath); err != nil {
		t.Fatalf("生成源码未落到原生应用目录 %q: %v", configuredPath, err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "tools", "generated", "controller", "user.go")); !os.IsNotExist(err) {
		t.Fatalf("console.auto_path 不得改变业务应用目录: %v", err)
	}

	if err := configuration.Set("console.auto_path", "../outside"); err != nil {
		t.Fatalf("写入非法 console.auto_path 失败: %v", err)
	}
	if _, err := applicationRelativeDirectory(app); err == nil {
		t.Fatal("console.auto_path 越出项目根目录时应被拒绝")
	}
}
