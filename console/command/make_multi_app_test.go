package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestMakeControllerSelectsApplicationWithThinkPHPAtSyntax 验证 make 命令使用
// ThinkPHP 的 应用名@类名 写法，并自动刷新该应用和根应用清单。
func TestMakeControllerSelectsApplicationWithThinkPHPAtSyntax(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	command := &MakeController{}
	command.SetApp(&framework.App{BasePath: basePath})

	if err := command.Execute(console.NewInput("admin@Account"), generatorTestOutput()); err != nil {
		t.Fatalf("按应用生成控制器失败: %v", err)
	}
	controllerSource := readGeneratedFile(t, basePath, "app", "admin", "controller", "account.go")
	if !strings.Contains(controllerSource, "type Account struct") {
		t.Fatalf("应用控制器内容错误:\n%s", controllerSource)
	}
	applicationSource := readGeneratedFile(t, basePath, "app", "admin", applicationDiscoveryFilename)
	if !strings.Contains(applicationSource, `Name: "admin"`) || !strings.Contains(applicationSource, "applicationController.Account") {
		t.Fatalf("admin 应用未装配新控制器:\n%s", applicationSource)
	}
	rootSource := readGeneratedFile(t, basePath, "app", applicationDiscoveryFilename)
	if !strings.Contains(rootSource, "applicationAdmin.Definition()") {
		t.Fatalf("根应用清单未发现 admin:\n%s", rootSource)
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "controller")); !os.IsNotExist(err) {
		t.Fatalf("原生多应用生成器不得回退到根 app/controller: %v", err)
	}
}

// TestMakeControllerDefaultsToConfiguredApplication 验证省略 @ 时使用
// default_app，默认配置缺失时与 ThinkPHP 一样选择 index。
func TestMakeControllerDefaultsToConfiguredApplication(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured string
		expected   string
	}{
		{name: "framework default", expected: "index"},
		{name: "configured default", configured: "portal", expected: "portal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeGeneratorTestModule(t, basePath)
			application := framework.NewAppUninitialized(basePath)
			t.Cleanup(func() { _ = application.Close() })
			if test.configured != "" {
				if err := application.Config().Set("app.default_app", test.configured); err != nil {
					t.Fatalf("设置默认应用失败: %v", err)
				}
			}
			command := &MakeController{}
			command.SetApp(application)
			if err := command.Execute(console.NewInput("Account"), generatorTestOutput()); err != nil {
				t.Fatalf("向默认应用生成控制器失败: %v", err)
			}
			if _, err := os.Stat(filepath.Join(basePath, "app", test.expected, "controller", "account.go")); err != nil {
				t.Fatalf("控制器未写入默认应用 %q: %v", test.expected, err)
			}
		})
	}
}

// TestMakeControllerSelectsApplicationWithOption 验证 --app 与 ThinkPHP 的
// application@class 语法等价，并在两种选择冲突时拒绝生成文件。
func TestMakeControllerSelectsApplicationWithOption(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	application := framework.NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })
	command := &MakeController{Command: console.Command{App: application}}
	command.Configure()
	input := console.NewInput("Account", "--app", "admin")
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析 --app 生成器选项失败: %v", err)
	}
	if err := command.Execute(input, generatorTestOutput()); err != nil {
		t.Fatalf("使用 --app 生成控制器失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "admin", "controller", "account.go")); err != nil {
		t.Fatalf("控制器未写入 --app 指定应用: %v", err)
	}

	conflict := console.NewInput("admin@Order", "--app", "index")
	if err := conflict.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析冲突生成器选项失败: %v", err)
	}
	if err := command.Execute(conflict, generatorTestOutput()); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("应用选择冲突必须被拒绝: %v", err)
	}
}
