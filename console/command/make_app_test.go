package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestMakeAppCreatesCompleteNativeApplication 验证应用生成入口保持完整并且不会覆盖业务源码。
func TestMakeAppCreatesCompleteNativeApplication(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	application := framework.NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })
	command := &MakeApp{Command: console.Command{App: application}}
	command.Configure()
	if command.GetSignature() != "make:app" {
		t.Fatalf("build 命令签名错误: %q", command.GetSignature())
	}
	arguments := command.GetArgumentDefinitions()
	if len(arguments) != 1 || arguments[0].Name != "app" || arguments[0].Required {
		t.Fatalf("build app 参数定义错误: %#v", arguments)
	}

	if err := command.Execute(console.NewInput("admin"), generatorTestOutput()); err != nil {
		t.Fatalf("构建 admin 应用失败: %v", err)
	}
	for _, relativePath := range []string{
		"app/admin/controller/base_controller.go",
		"app/admin/controller/index.go",
		"app/admin/event.go",
		"app/admin/middleware.go",
		"app/admin/provider.go",
		"app/admin/service.go",
		"app/admin/route/app.go",
		"app/admin/view/index.html",
		"app/admin/autoload_generated.go",
		"app/autoload_generated.go",
	} {
		if _, err := os.Stat(filepath.Join(basePath, filepath.FromSlash(relativePath))); err != nil {
			t.Errorf("build 未创建 %s: %v", relativePath, err)
		}
	}
	if err := CheckControllerDiscovery(application); err != nil {
		t.Fatalf("build 后静态发现清单无效: %v", err)
	}
	controllerPath := filepath.Join(basePath, "app", "admin", "controller", "index.go")
	original, err := os.ReadFile(controllerPath)
	if err != nil {
		t.Fatalf("读取默认控制器失败: %v", err)
	}
	customized := append(original, []byte("\n// 业务自定义内容。\n")...)
	if err := os.WriteFile(controllerPath, customized, 0o644); err != nil {
		t.Fatalf("修改默认控制器失败: %v", err)
	}
	if err := command.Execute(console.NewInput("admin"), generatorTestOutput()); err != nil {
		t.Fatalf("重复构建 admin 应用失败: %v", err)
	}
	after, err := os.ReadFile(controllerPath)
	if err != nil || string(after) != string(customized) {
		t.Fatalf("重复 build 覆盖了业务源码: err=%v\n%s", err, after)
	}
}

// TestBuildDefaultsToIndexAndRejectsUnsafeApplication 验证无参数时构建
// default_app，非法应用名不会在 app 目录留下文件。
func TestMakeAppDefaultsToIndexAndRejectsUnsafeApplication(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	application := framework.NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })
	command := &MakeApp{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatalf("构建默认应用失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "index", "controller", "index.go")); err != nil {
		t.Fatalf("默认 build 未创建 index 应用: %v", err)
	}
	if err := command.Execute(console.NewInput("../unsafe"), generatorTestOutput()); err == nil || !strings.Contains(err.Error(), "应用名") {
		t.Fatalf("非法应用名应被拒绝: %v", err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "unsafe")); !os.IsNotExist(err) {
		t.Fatalf("非法 build 不得创建根目录外文件: %v", err)
	}
}

// TestBuildRollsBackOnlyFilesCreatedByCurrentCommand 验证已有业务文件无效时，
// build 返回发现错误、保留原文件，并撤销本次创建的其余脚手架文件。
func TestMakeAppRollsBackOnlyFilesCreatedByCurrentCommand(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	invalidSource := "package wrong\n\n// Existing 是构建前已有的业务内容。\ntype Existing struct{}\n"
	writeDiscoveryFixture(t, basePath, "app/admin/event.go", invalidSource)
	application := framework.NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })
	command := &MakeApp{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput("admin"), generatorTestOutput()); err == nil || !strings.Contains(err.Error(), "包名必须为 admin") {
		t.Fatalf("无效已有源码应阻止 build: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(basePath, "app", "admin", "event.go"))
	if err != nil || string(content) != invalidSource {
		t.Fatalf("build 回滚破坏了已有业务文件: err=%v content=%q", err, content)
	}
	for _, relativePath := range []string{
		"app/admin/controller/index.go",
		"app/admin/middleware.go",
		"app/admin/provider.go",
		"app/admin/service.go",
		"app/admin/route/app.go",
		"app/admin/view/index.html",
		"app/admin/autoload_generated.go",
		"app/autoload_generated.go",
	} {
		if _, err := os.Stat(filepath.Join(basePath, filepath.FromSlash(relativePath))); !os.IsNotExist(err) {
			t.Errorf("失败 build 遗留文件 %s: %v", relativePath, err)
		}
	}
}
