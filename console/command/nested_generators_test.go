package command

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestNestedGeneratorsProduceUsableApplication 验证分层控制器、模型和验证器会进入真实编译注册清单。
func TestNestedGeneratorsProduceUsableApplication(t *testing.T) {
	base := t.TempDir()
	writeGeneratorTestModule(t, base)
	app := framework.NewAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	cli := console.NewConsole(app)
	for _, command := range []console.ICommand{&MakeController{}, &MakeModel{}, &MakeValidate{}} {
		if err := cli.Register(command); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"make:controller", "admin@account/User"}, {"make:model", "admin@account/User"}, {"make:validate", "admin@account/User"}} {
		if err := cli.Run(args...); err != nil {
			t.Fatalf("生成分层组件失败: %v", err)
		}
	}
	generated := readGeneratedFile(t, base, "app", "admin", applicationDiscoveryFilename)
	if strings.Count(generated, `"account.User"`) != 3 {
		t.Fatalf("嵌套组件没有完整注册: %s", generated)
	}
	for _, layer := range []string{"controller", "model", "validate"} {
		content := readGeneratedFile(t, base, "app", "admin", layer, "account", "user.go")
		if !strings.Contains(content, "package account") {
			t.Fatalf("生成包名不匹配目标目录: %s", content)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./...")
	command.Dir = base
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("生成项目无法编译: %v\n%s", err, output)
	}
}

// TestNestedGeneratorRejectsTraversalBeforeWriting 验证子包支持不会放宽路径穿越保护。
func TestNestedGeneratorRejectsTraversalBeforeWriting(t *testing.T) {
	for _, name := range []string{"admin@../User", "admin@account/../User", "admin@account//User", "admin@/User", `admin@C:\User`, "admin@account/./User"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			app := framework.NewAppUninitialized(base)
			t.Cleanup(func() { _ = app.Close() })
			command := &MakeController{Command: console.Command{App: app}}
			if err := command.Execute(console.NewInput(name), generatorTestOutput()); err == nil {
				t.Fatalf("危险名称未被拒绝: %s", name)
			}
			if matches, err := filepath.Glob(filepath.Join(base, "app", "*")); err != nil || len(matches) != 0 {
				t.Fatalf("拒绝前产生了应用文件: %v %v", matches, err)
			}
		})
	}
}
