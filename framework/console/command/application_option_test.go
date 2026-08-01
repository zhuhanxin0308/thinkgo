package command

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/console"
)

func TestGeneratorApplicationOptionScopesOutputToSelectedApplication(t *testing.T) {
	basePath := t.TempDir()
	manager, err := framework.NewApplicationManagerFromDefinitions(basePath, []framework.ApplicationDefinition{
		{Name: "index", Path: "app/index", Register: func(*framework.App) error { return nil }},
		{Name: "admin", Path: "app/admin", Register: func(*framework.App) error { return nil }},
	}, true)
	if err != nil {
		t.Fatalf("创建测试应用管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	command := &MakeModel{Command: console.Command{App: manager.DefaultApplication()}}
	command.SetApplicationManager(manager)
	command.Configure()
	input := console.NewInput("--app", "admin", "UserProfile")
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析 --app 失败: %v", err)
	}
	if err := command.Execute(input, console.NewOutputWithWriters(io.Discard, io.Discard, false)); err != nil {
		t.Fatalf("按应用生成模型失败: %v", err)
	}

	admin, ok := manager.Application("admin")
	if !ok {
		t.Fatal("测试应用 admin 不存在")
	}
	generated := filepath.Join(admin.ApplicationPath, "model", "user_profile.go")
	if _, err := os.Stat(generated); err != nil {
		t.Fatalf("模型没有写入目标应用目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "model", "user_profile.go")); !os.IsNotExist(err) {
		t.Fatalf("生成器不应创建根级旧目录文件: %v", err)
	}
}

func TestGeneratorApplicationOptionRejectsUnknownApplication(t *testing.T) {
	manager, err := framework.NewApplicationManagerFromDefinitions(t.TempDir(), []framework.ApplicationDefinition{
		{Name: "index", Path: "app/index", Register: func(*framework.App) error { return nil }},
	}, true)
	if err != nil {
		t.Fatalf("创建测试应用管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	command := &MakeValidate{Command: console.Command{App: manager.DefaultApplication()}}
	command.SetApplicationManager(manager)
	command.Configure()
	input := console.NewInput("--app", "../outside", "User")
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析应用参数失败: %v", err)
	}
	if err := command.Execute(input, console.NewOutputWithWriters(io.Discard, io.Discard, false)); err == nil {
		t.Fatal("越界应用名必须被拒绝")
	}
}
