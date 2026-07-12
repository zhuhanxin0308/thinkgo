package console

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type hardeningCommand struct {
	Command
	name       string
	executeErr error
	executed   bool
}

func (c *hardeningCommand) Configure() {
	c.Signature = c.name
	c.Description = "测试命令 " + c.name
	if c.name == "required" {
		c.AddArgument("name", "目标名称", true)
		c.AddOption("port", "p", "监听端口", "8080")
		c.AddBoolOption("verbose", "v", "显示详细信息")
	}
}

func (c *hardeningCommand) Execute(_ *Input, output *Output) error {
	c.executed = true
	output.Writeln("executed " + c.name)
	return c.executeErr
}

// TestConsoleRejectsInvalidAndDuplicateCommands 验证命令注册不会因 nil、非法签名
// 或重复名称发生 panic、静默覆盖和不可预测调度。
func TestConsoleRejectsInvalidAndDuplicateCommands(t *testing.T) {
	cli := NewConsole(nil)
	var typedNil *hardeningCommand
	for _, command := range []ICommand{nil, typedNil, &hardeningCommand{name: "bad command"}} {
		if err := cli.Register(command); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("非法命令应返回 ErrInvalidCommand，实际为 %v", err)
		}
	}

	if err := cli.Register(&hardeningCommand{name: "alpha"}); err != nil {
		t.Fatalf("注册合法命令失败: %v", err)
	}
	if err := cli.Register(&hardeningCommand{name: "alpha"}); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("重复命令应返回 ErrDuplicateCommand，实际为 %v", err)
	}
	required := &hardeningCommand{name: "required"}
	if err := cli.Register(required); err != nil {
		t.Fatalf("注册带参数命令失败: %v", err)
	}
	if err := cli.Register(required); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("同一带参数命令重复注册应返回 ErrDuplicateCommand，实际为 %v", err)
	}
}

// TestConsoleRunPropagatesInputCommandAndLookupErrors 验证 CLI 失败会返回给入口层，
// 使脚本和 CI 能得到非零退出码，而不是仅打印文本后假装成功。
func TestConsoleRunPropagatesInputCommandAndLookupErrors(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cli := NewConsole(nil)
	if err := cli.SetOutput(NewOutputWithWriters(stdout, stderr, false)); err != nil {
		t.Fatalf("设置测试输出失败: %v", err)
	}

	required := &hardeningCommand{name: "required"}
	if err := cli.Register(required); err != nil {
		t.Fatalf("注册必填参数命令失败: %v", err)
	}
	if err := cli.Run("required"); !errors.Is(err, ErrInvalidInput) || required.executed {
		t.Fatalf("缺少必填参数应在执行前失败: executed=%v err=%v", required.executed, err)
	}

	executionErr := errors.New("业务命令失败")
	failing := &hardeningCommand{name: "failing", executeErr: executionErr}
	if err := cli.Register(failing); err != nil {
		t.Fatalf("注册失败命令失败: %v", err)
	}
	if err := cli.Run("failing"); !errors.Is(err, executionErr) || !failing.executed {
		t.Fatalf("命令错误应原样传播: executed=%v err=%v", failing.executed, err)
	}

	err := cli.Run("missing\x1b[2J")
	if !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("未知命令应返回 ErrCommandNotFound，实际为 %v", err)
	}
	if strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("未知命令错误不得包含原始终端控制字符: %q", err)
	}
}

// TestConsoleHelpIsDeterministic 验证帮助输出按命令名排序，避免 map 遍历顺序
// 导致文档、快照和人工排障结果随机变化。
func TestConsoleHelpIsDeterministic(t *testing.T) {
	stdout := &bytes.Buffer{}
	cli := NewConsole(nil)
	if err := cli.SetOutput(NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("设置测试输出失败: %v", err)
	}
	for _, name := range []string{"zeta", "alpha", "middle"} {
		if err := cli.Register(&hardeningCommand{name: name}); err != nil {
			t.Fatalf("注册命令 %s 失败: %v", name, err)
		}
	}
	if err := cli.Run(); err != nil {
		t.Fatalf("展示帮助失败: %v", err)
	}
	text := stdout.String()
	alpha := strings.Index(text, "alpha")
	middle := strings.Index(text, "middle")
	zeta := strings.Index(text, "zeta")
	if alpha < 0 || middle <= alpha || zeta <= middle {
		t.Fatalf("帮助命令顺序不稳定: %q", text)
	}
}

type consoleFailingWriter struct{ err error }

func (writer *consoleFailingWriter) Write([]byte) (int, error) { return 0, writer.err }

// TestOutputSeparatesStreamsAndReportsWriteFailure 验证错误输出进入 stderr，
// 控制字符被转义，且断管等写入失败能返回给 Console.Run。
func TestOutputSeparatesStreamsAndReportsWriteFailure(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	output := NewOutputWithWriters(stdout, stderr, false)
	output.Info("ready")
	output.Error("bad\n\x1b[2J")
	if !strings.Contains(stdout.String(), "ready") || strings.Contains(stdout.String(), "bad") {
		t.Fatalf("标准输出内容错误: %q", stdout.String())
	}
	if strings.ContainsRune(stderr.String(), '\x1b') || strings.Contains(stderr.String(), "bad\n") {
		t.Fatalf("错误输出包含未转义控制字符: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `bad\n\x1b[2J`) {
		t.Fatalf("错误输出未保留可读的转义信息: %q", stderr.String())
	}

	writeErr := errors.New("broken pipe")
	failed := NewOutputWithWriters(&consoleFailingWriter{err: writeErr}, stderr, false)
	failed.Writeln("data")
	if !errors.Is(failed.Err(), writeErr) {
		t.Fatalf("输出写入错误应可观察，实际为 %v", failed.Err())
	}
}

// TestOutputColorRawWritesAndInvalidStreams 验证颜色只包装状态输出，原始写入
// 保持不变，控制字符被转义，且空流或空接收者返回明确错误。
func TestOutputColorRawWritesAndInvalidStreams(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	output := NewOutputWithWriters(stdout, stderr, true)
	output.Write("raw")
	output.Warning("warn\tvalue")
	output.Success("done")
	if !strings.HasPrefix(stdout.String(), "raw") || !strings.Contains(stdout.String(), "\x1b[33mwarn\\tvalue\x1b[0m") ||
		!strings.Contains(stdout.String(), "\x1b[32mdone\x1b[0m") {
		t.Fatalf("颜色或原始输出格式错误: %q", stdout.String())
	}
	if stderr.Len() != 0 || output.Err() != nil {
		t.Fatalf("正常输出不应写入 stderr 或产生错误: stderr=%q err=%v", stderr.String(), output.Err())
	}

	if err := NewOutputWithWriters(nil, stderr, false).Err(); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("空 stdout 应返回 ErrInvalidOutput，实际为 %v", err)
	}
	if err := NewOutputWithWriters(stdout, nil, false).Err(); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("空 stderr 应返回 ErrInvalidOutput，实际为 %v", err)
	}
	var nilOutput *Output
	if !errors.Is(nilOutput.Err(), ErrInvalidOutput) {
		t.Fatalf("空 Output 接收者应返回 ErrInvalidOutput")
	}
	automaticStdout := &bytes.Buffer{}
	automatic := NewOutputWithAutoColor(automaticStdout, &bytes.Buffer{})
	automatic.Info("piped")
	if strings.ContainsRune(automaticStdout.String(), '\x1b') {
		t.Fatalf("非终端自动输出不应包含 ANSI 序列: %q", automaticStdout.String())
	}
}

// TestConsoleHelpShowsArgumentsOptionsAndDefaults 验证帮助输出包含必填参数、
// 长短选项及默认值，供脚本调用方稳定发现命令契约。
func TestConsoleHelpShowsArgumentsOptionsAndDefaults(t *testing.T) {
	stdout := &bytes.Buffer{}
	cli := NewConsole(nil)
	if err := cli.SetOutput(NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("设置测试输出失败: %v", err)
	}
	if err := cli.Register(&hardeningCommand{name: "required"}); err != nil {
		t.Fatalf("注册命令失败: %v", err)
	}
	if err := cli.ShowHelp(); err != nil {
		t.Fatalf("展示帮助失败: %v", err)
	}
	for _, expected := range []string{"<name>", "required", "-p, --port", "default: 8080", "-v, --verbose"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("帮助输出缺少 %q: %q", expected, stdout.String())
		}
	}
	if err := cli.SetOutput(nil); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("设置空输出应返回 ErrInvalidOutput，实际为 %v", err)
	}
}
