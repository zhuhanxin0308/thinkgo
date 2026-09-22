package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/console/command"
)

// 编译期断言保证原有宿主保存 Run 函数值的代码保持兼容。
var _ func(context.Context, string, []string, io.Writer, io.Writer, func(*framework.App) error) error = Run

type auditTestCommand struct {
	console.Command
	executed       bool
	executionError error
}

func (c *auditTestCommand) Configure() {
	c.Signature = "audit:probe"
	c.Description = "审计探测命令"
	c.AddArgument("name", "审计名称", true)
}

func (c *auditTestCommand) Execute(input *console.Input, output *console.Output) error {
	c.executed = true
	output.Writeln(input.GetArgument(0))
	return c.executionError
}

type databaseAuditTestCommand struct {
	auditTestCommand
	required bool
}

func (c *databaseAuditTestCommand) RequiresDatabase() bool { return c.required }

// TestProjectCommandInformationDoesNotBootBusiness 验证自定义帮助、列表和未知命令不会调用业务装配或读取损坏配置。
func TestProjectCommandInformationDoesNotBootBusiness(t *testing.T) {
	base := t.TempDir()
	writeProjectCommandFixture(t, base, "config/app.json", "broken")
	registered := false
	register := func(*framework.App) error { registered = true; return errors.New("不应装配") }
	for _, args := range [][]string{{"list"}, {"help", "audit:probe"}, {"audit:probe", "--help"}, {"missing"}, {"audit:probe"}} {
		var output bytes.Buffer
		current := &auditTestCommand{}
		err := RunWithCommands(context.Background(), base, args, &output, &output, register, current)
		if registered || current.executed {
			t.Fatalf("%v 意外执行业务", args)
		}
		if args[0] == "missing" {
			if !errors.Is(err, console.ErrCommandNotFound) {
				t.Fatalf("未知命令错误不正确: %v", err)
			}
		} else if len(args) == 1 && args[0] == "audit:probe" {
			if err == nil {
				t.Fatal("缺少必要参数未被拒绝")
			}
		} else if err != nil || !bytes.Contains(output.Bytes(), []byte("audit:probe")) {
			t.Fatalf("%v 自定义命令信息缺失: %v %s", args, err, output.String())
		}
	}
}

// TestProjectCommandConflictsAreRejectedBeforeBusiness 验证重复定义和内置签名冲突在业务启动前返回明确错误。
func TestProjectCommandConflictsAreRejectedBeforeBusiness(t *testing.T) {
	register := func(*framework.App) error { t.Fatal("冲突命令不应装配业务"); return nil }
	for _, commands := range [][]console.ICommand{
		{&auditTestCommand{}, &auditTestCommand{}},
		{&console.Command{Signature: "list", Description: "重复内置"}},
		{nil},
	} {
		err := RunWithCommands(context.Background(), t.TempDir(), []string{"list"}, io.Discard, io.Discard, register, commands...)
		if !errors.Is(err, console.ErrDuplicateCommand) && !errors.Is(err, console.ErrInvalidCommand) {
			t.Fatalf("冲突或无效命令未失败: %v", err)
		}
	}
}

// TestProjectCommandEmbeddedExecution 验证静态嵌入命令完成业务装配、App 注入、执行及错误传播。
func TestProjectCommandEmbeddedExecution(t *testing.T) {
	base := t.TempDir()
	writeProjectCommandConfig(t, base)
	t.Chdir(base)
	want := errors.New("审计执行错误")
	for _, executeErr := range []error{nil, want} {
		current := &auditTestCommand{executionError: executeErr}
		registered := false
		register := func(app *framework.App) error { registered = app != nil; return nil }
		var output bytes.Buffer
		err := RunWithCommands(context.Background(), "", []string{"audit:probe", "Alice"}, &output, &output, register, current)
		if !errors.Is(err, executeErr) || !registered || !current.executed || current.App == nil || !strings.Contains(output.String(), "Alice") {
			t.Fatalf("嵌入命令生命周期错误: %v registered=%v executed=%v %s", err, registered, current.executed, output.String())
		}
	}
	for _, required := range []bool{false, true} {
		current := &databaseAuditTestCommand{required: required}
		current.Configure()
		_, err := buildApplication(base, []string{"audit:probe", "Alice"}, func(*framework.App) error { return want }, current)
		if !errors.Is(err, want) {
			t.Fatalf("数据库依赖=%v 时装配错误未传播: %v", required, err)
		}
	}
}

// TestProjectCommandStandaloneHost 验证独立 CLI 真实编译项目命令并支持内部服务依赖，帮助和未知命令不装配业务。
func TestProjectCommandStandaloneHost(t *testing.T) {
	base := projectCommandTestModule(t)
	writeProjectCommandFixture(t, base, "app/index/command/audit.go", projectAuditCommandSource)
	writeProjectCommandFixture(t, base, "internal/service/name.go", "package service\nconst Name = \"probe\"\n")
	writeProjectCommandFixture(t, base, "app/register.go", `package app
import (
    "errors"
    "github.com/zhuhanxin0308/thinkgo/v3"
)
func Register(*framework.App) error { return errors.New("业务装配标记") }
`)
	writeProjectCommandFixture(t, base, "config/app.json", "broken")
	for _, args := range [][]string{{"list"}, {"help", "audit:probe"}, {"audit:probe", "--help"}, {"missing"}, {"audit:probe"}, {"audit:probe", "Alice"}} {
		var output bytes.Buffer
		err := Run(context.Background(), base, args, &output, &output, nil)
		text := output.String()
		if len(args) == 2 && args[1] == "Alice" {
			if err == nil || !strings.Contains(text, "业务装配标记") {
				t.Fatalf("真实自定义命令未进入业务装配: %v %s", err, text)
			}
		} else if strings.Contains(text, "业务装配标记") {
			t.Fatalf("%v 提前装配业务: %v %s", args, err, text)
		} else if args[0] == "missing" || len(args) == 1 && args[0] == "audit:probe" {
			if err == nil {
				t.Fatalf("%v 应拒绝无效调用", args)
			}
		} else if err != nil || !strings.Contains(text, "audit:probe") {
			t.Fatalf("真实项目命令信息缺失: %v %s", err, text)
		}
	}
	writeProjectCommandFixture(t, base, "app/register.go", projectRegistrationSource)
	writeProjectCommandConfig(t, base)
	var output bytes.Buffer
	if err := Run(context.Background(), base, []string{"audit:probe", "Alice"}, &output, &output, nil); err != nil || !strings.Contains(output.String(), "审计完成:Alice") {
		t.Fatalf("真实项目命令执行失败: %v %s", err, output.String())
	}
}

// TestProjectCommandHostFailures 验证缺失源码、取消和编译失败都可观察且不会执行项目业务。
func TestProjectCommandHostFailures(t *testing.T) {
	base := projectCommandTestModule(t)
	writeProjectCommandFixture(t, base, "app/index/command/broken.go", `package command
import "github.com/zhuhanxin0308/thinkgo/v3/console"
type Broken struct { console.Command }
func (c *Broken) Configure() { c.Signature = "audit:broken"; c.Description = "损坏命令" }
func (*Broken) Execute(*console.Input, *console.Output) error { return missingBusinessFunction() }
`)
	var output bytes.Buffer
	if err := Run(context.Background(), base, []string{"audit:broken"}, &output, &output, nil); err == nil || !strings.Contains(output.String(), "missingBusinessFunction") {
		t.Fatalf("函数体编译错误被忽略: %v %s", err, output.String())
	}
	commands := []command.ProjectCommandType{{ImportPath: "example.com/project/app/index/command", TypeName: "Broken"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, business := range []bool{false, true} {
		if err := runProjectCommandHost(ctx, base, nil, io.Discard, io.Discard, commands, business); !errors.Is(err, context.Canceled) {
			t.Fatalf("宿主流程未响应取消: %v", err)
		}
	}
	if err := RunWithCommands(context.Background(), t.TempDir(), []string{"audit:probe", "Alice"}, io.Discard, io.Discard, nil, &auditTestCommand{}); !errors.Is(err, ErrProjectCommandRequiresBusiness) {
		t.Fatalf("缺少业务回调未请求宿主交接: %v", err)
	}
	writeProjectCommandFixture(t, base, "app/index/command/broken.go", "package command\ntype Broken struct {\n")
	if err := Run(context.Background(), base, []string{"list"}, io.Discard, io.Discard, nil); err == nil {
		t.Fatal("发现错误未传播")
	}
}

// TestProjectCommandCancellationStopsExecution 验证取消直接终止业务宿主，后续写入不发生且两轮临时目录均清理。
func TestProjectCommandCancellationStopsExecution(t *testing.T) {
	base := projectCommandTestModule(t)
	writeProjectCommandConfig(t, base)
	writeProjectCommandFixture(t, base, "app/register.go", projectRegistrationSource)
	writeProjectCommandFixture(t, base, "app/index/command/wait.go", `package command
import (
    "os"
    "time"
    "github.com/zhuhanxin0308/thinkgo/v3/console"
)
type Wait struct { console.Command }
func (c *Wait) Configure() { c.Signature = "audit:wait"; c.Description = "等待取消" }
func (*Wait) Execute(*console.Input, *console.Output) error {
    if err := os.WriteFile("started", []byte("started"), 0600); err != nil { return err }
    time.Sleep(500 * time.Millisecond)
    return os.WriteFile("finished", []byte("finished"), 0600)
}
`)
	temporary := t.TempDir()
	t.Setenv("TMP", temporary)
	t.Setenv("TEMP", temporary)
	t.Setenv("TMPDIR", temporary)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- Run(ctx, base, []string{"audit:wait"}, io.Discard, io.Discard, nil) }()
	deadline := time.After(90 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-finished:
			t.Fatalf("业务启动前提前退出: %v", err)
		case <-deadline:
			t.Fatal("业务宿主没有启动")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(base, "started")); err == nil {
				cancel()
				goto canceled
			}
		}
	}
canceled:
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误未传播: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(base, "finished")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("取消后业务进程继续执行: %v", err)
	}
	if paths, err := filepath.Glob(filepath.Join(temporary, "thinkgo-commands-*")); err != nil || len(paths) != 0 {
		t.Fatalf("取消后命令宿主目录残留: %v %v", paths, err)
	}
}

func projectCommandTestModule(t *testing.T) string {
	t.Helper()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("CGO_ENABLED", "0")
	base := t.TempDir()
	frameworkRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(frameworkRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(module), "module github.com/zhuhanxin0308/thinkgo/v3", "module example.com/project", 1)
	content += "\nrequire github.com/zhuhanxin0308/thinkgo/v3 v3.0.0\nreplace github.com/zhuhanxin0308/thinkgo/v3 => " + filepath.ToSlash(frameworkRoot) + "\n"
	writeProjectCommandFixture(t, base, "go.mod", content)
	checksums, err := os.ReadFile(filepath.Join(frameworkRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	writeProjectCommandFixture(t, base, "go.sum", string(checksums))
	return base
}

func writeProjectCommandConfig(t *testing.T, base string) {
	t.Helper()
	for path, source := range map[string]string{
		"config/app.json":     `{"app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`,
		"config/log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"config/cache.json":   `{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`,
		"config/view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"config/cookie.json":  `{}`,
		"config/session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
		"public/.keep":        "",
	} {
		writeProjectCommandFixture(t, base, path, source)
	}
}

func writeProjectCommandFixture(t *testing.T, base, path, source string) {
	t.Helper()
	filename := filepath.Join(base, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

const projectRegistrationSource = `package app
import "github.com/zhuhanxin0308/thinkgo/v3"
func Register(*framework.App) error { return nil }
`

const projectAuditCommandSource = `package command
import (
    "example.com/project/internal/service"
    "github.com/zhuhanxin0308/thinkgo/v3/console"
)
type Audit struct { console.Command }
func (c *Audit) Configure() {
    c.Signature = "audit:" + service.Name
    c.Description = "审计探测命令"
    c.AddArgument("name", "审计名称", true)
}
func (c *Audit) Execute(input *console.Input, output *console.Output) error {
    output.Writeln("审计完成:" + input.GetArgument(0))
    return nil
}
`
