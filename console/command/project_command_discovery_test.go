package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestProjectCommandDiscoveryUsesStaticTypes 验证多应用和嵌套命令按真实接口发现，排除辅助类型与未实例化泛型。
func TestProjectCommandDiscoveryUsesStaticTypes(t *testing.T) {
	base := t.TempDir()
	writeDiscoveryModuleFixture(t, base, "example.com/commands")
	writeDiscoveryFixture(t, base, "app/index/command/audit.go", `package command
import "github.com/zhuhanxin0308/thinkgo/v3/console"
type Audit struct { console.Command }
func (c *Audit) Configure() { c.Signature = "audit:probe"; c.Description = "审计探测" }
func (*Audit) Execute(*console.Input, *console.Output) error { return nil }
type Helper struct{}
type Generic[T any] struct { console.Command }
type OtherHelper struct{}
`)
	writeDiscoveryFixture(t, base, "app/admin/command/report/daily.go", `package report
import "github.com/zhuhanxin0308/thinkgo/v3/console"
type Daily struct { console.Command }
func (c *Daily) Configure() { c.Signature = "report:daily"; c.Description = "每日报告" }
func (*Daily) Execute(*console.Input, *console.Output) error { return nil }
`)
	writeDiscoveryFixture(t, base, "app/index/command/internal/helper/helper.go", `package helper
import "github.com/zhuhanxin0308/thinkgo/v3/console"
type Private struct { console.Command }
`)
	writeDiscoveryFixture(t, base, "app/index/command/testdata/fixture.go", "package fixture\ntype Fixture struct{}\n")
	writeDiscoveryFixture(t, base, "app/internal/command/private.go", `package command
import "github.com/zhuhanxin0308/thinkgo/v3/console"
type Private struct { console.Command }
`)
	writeDiscoveryFixture(t, base, "app/index/command/excluded.go", "//go:build thinkgo_missing_command_tag\n\npackage command\nthis cannot compile\n")
	commands, err := DiscoverProjectCommands(base)
	if err != nil || len(commands) != 2 {
		t.Fatalf("命令发现失败: %#v %v", commands, err)
	}
	if commands[0].TypeName != "Daily" || commands[1].TypeName != "Audit" {
		t.Fatalf("命令类型或顺序错误: %#v", commands)
	}
	application := &framework.App{BasePath: base, ApplicationPath: filepath.Join(base, "app")}
	if err := RefreshControllerDiscovery(application); err != nil {
		t.Fatal(err)
	}
	source := readGeneratedDiscoverySource(t, base, projectCommandDiscoveryPath)
	for _, want := range []string{"func Commands() []console.ICommand", "&projectCommand0.Daily{}", "&projectCommand1.Audit{}"} {
		if !strings.Contains(source, want) {
			t.Fatalf("静态命令清单缺少 %q:\n%s", want, source)
		}
	}
	if err := CheckControllerDiscovery(application); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFixture(t, base, projectCommandDiscoveryPath, "过期清单")
	if err := CheckControllerDiscovery(application); err == nil {
		t.Fatal("过期命令清单未被检测")
	}
}

// TestProjectCommandDiscoveryPathsAndTypes 验证空目录、非法模块、类型错误和目录替换均有确定行为。
func TestProjectCommandDiscoveryPathsAndTypes(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		source string
		valid  bool
	}{
		{name: "没有应用目录", valid: true},
		{name: "空命令包", path: "app/index/command/helper.go", source: "package command\ntype Helper struct{}\n", valid: true},
		{name: "应用路径是文件", path: "app", source: "不是目录"},
		{name: "命令路径是文件", path: "app/index/command", source: "不是目录"},
		{name: "非法模块声明", path: "go.mod", source: "不是模块声明"},
		{name: "类型不合法", path: "app/index/command/broken.go", source: "package command\ntype Broken MissingType\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			writeDiscoveryModuleFixture(t, base, "example.com/commands")
			if test.path != "" {
				writeDiscoveryFixture(t, base, test.path, test.source)
			}
			commands, err := DiscoverProjectCommands(base)
			if test.valid && (err != nil || len(commands) != 0) || !test.valid && err == nil {
				t.Fatalf("发现结果不符合契约: %#v %v", commands, err)
			}
		})
	}
	if _, err := DiscoverProjectCommands(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失项目根目录错误不正确: %v", err)
	}
}

// TestMakeCommandRefreshesAndRollsBack 验证创建命令立即进入静态清单，刷新失败时回滚且保留既有文件。
func TestMakeCommandRefreshesAndRollsBack(t *testing.T) {
	base := t.TempDir()
	writeDiscoveryModuleFixture(t, base, "example.com/commands")
	current := &MakeCommand{}
	current.SetApp(&framework.App{BasePath: base})
	current.Configure()
	var output bytes.Buffer
	if err := current.Execute(console.NewInput("Audit", "audit:probe"), console.NewOutputWithWriters(&output, &output, false)); err != nil {
		t.Fatal(err)
	}
	if source := readGeneratedDiscoverySource(t, base, projectCommandDiscoveryPath); !strings.Contains(source, ".Audit{}") {
		t.Fatalf("新命令未进入静态清单: %s", source)
	}
	writeDiscoveryFixture(t, base, "app/index/command/broken.go", "package command\ntype Broken struct {\n")
	if err := current.Execute(console.NewInput("Later", "audit:later"), console.NewOutputWithWriters(&output, &output, false)); err == nil {
		t.Fatal("其他源码损坏时命令生成应失败")
	}
	if _, err := os.Stat(filepath.Join(base, "app/index/command/later.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("刷新失败后未回滚新文件: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "app/index/command/audit.go")); err != nil {
		t.Fatalf("既有命令被影响: %v", err)
	}
}

// TestProjectCommandDiscoveryRejectsBrokenSource 验证源码错误必须显式返回，不能回退到旧清单。
func TestProjectCommandDiscoveryRejectsBrokenSource(t *testing.T) {
	base := t.TempDir()
	if commands, err := DiscoverProjectCommands(base); err != nil || len(commands) != 0 {
		t.Fatalf("非项目目录应无命令: %#v %v", commands, err)
	}
	writeDiscoveryModuleFixture(t, base, "example.com/commands")
	writeDiscoveryFixture(t, base, "app/index/command/broken.go", "package command\ntype Broken struct {\n")
	if _, err := DiscoverProjectCommands(base); err == nil {
		t.Fatal("命令源码语法错误被忽略")
	}
}
