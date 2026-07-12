package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"thinkgo/framework/console"
)

// TestRunConsoleExecutesCommandAndPropagatesFailures 验证入口显式传递参数、
// 成功命令写入 stdout，未知命令和参数错误返回给 main 生成非零退出码。
func TestRunConsoleExecutesCommandAndPropagatesFailures(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := runConsole([]string{"version"}, stdout, stderr); err != nil {
		t.Fatalf("执行 version 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "ThinkGo Framework") || stderr.Len() != 0 {
		t.Fatalf("version 输出错误: stdout=%q stderr=%q", stdout, stderr)
	}

	stdout.Reset()
	unknownErr := runConsole([]string{"missing\x1b[2J"}, stdout, stderr)
	if !errors.Is(unknownErr, console.ErrCommandNotFound) {
		t.Fatalf("未知命令应返回 ErrCommandNotFound，实际为 %v", unknownErr)
	}
	if strings.ContainsRune(unknownErr.Error(), '\x1b') {
		t.Fatalf("入口错误不得包含原始终端控制字符: %q", unknownErr)
	}

	missingArgumentErr := runConsole([]string{"make:model"}, stdout, stderr)
	if !errors.Is(missingArgumentErr, console.ErrInvalidInput) {
		t.Fatalf("生成器缺少名称应返回 ErrInvalidInput，实际为 %v", missingArgumentErr)
	}
}

// TestNewConsoleAppUsesExplicitCommand 验证初始化模式不依赖全局 os.Args，
// 非 run 命令不会建立数据库连接。
func TestNewConsoleAppUsesExplicitCommand(t *testing.T) {
	app := newConsoleApp([]string{"version"})
	if app == nil {
		t.Fatal("命令行应用不能为空")
	}
	defer func() {
		if err := app.Close(); err != nil {
			t.Errorf("关闭命令行应用失败: %v", err)
		}
	}()
	if app.DB != nil || app.DBManager == nil {
		t.Fatalf("非 run 命令应保留空管理器但不建立默认连接: DB=%#v manager=%#v", app.DB, app.DBManager)
	}
	if _, err := app.DBManager.Default(); err == nil {
		t.Fatal("非 run 命令的空管理器不应返回默认数据库连接")
	}
}

type failingConsoleWriter struct{ err error }

func (writer failingConsoleWriter) Write([]byte) (int, error) { return 0, writer.err }

// TestRunConsoleHandlesHelpInvalidStreamsAndBrokenPipes 验证空参数正常展示帮助，
// 无效输出流和写入失败会传播到入口层。
func TestRunConsoleHandlesHelpInvalidStreamsAndBrokenPipes(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := runConsole(nil, stdout, stderr); err != nil {
		t.Fatalf("展示命令帮助失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "Available commands") || !strings.Contains(stdout.String(), "make:controller") {
		t.Fatalf("帮助输出不完整: %q", stdout.String())
	}
	if err := runConsole([]string{"version"}, nil, stderr); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空 stdout 应返回 ErrInvalidOutput，实际为 %v", err)
	}
	writeErr := errors.New("broken pipe")
	if err := runConsole([]string{"version"}, failingConsoleWriter{err: writeErr}, stderr); !errors.Is(err, writeErr) {
		t.Fatalf("标准输出写入错误应传播，实际为 %v", err)
	}
}

// TestRegisterDefaultCommandsRejectsInvalidConsoleAndDuplicates 验证内置命令
// 注册不会忽略空 Console 或重复签名。
func TestRegisterDefaultCommandsRejectsInvalidConsoleAndDuplicates(t *testing.T) {
	if err := registerDefaultCommands(nil); !errors.Is(err, console.ErrInvalidCommand) {
		t.Fatalf("空 Console 应返回 ErrInvalidCommand，实际为 %v", err)
	}
	cli := console.NewConsole(nil)
	if err := registerDefaultCommands(cli); err != nil {
		t.Fatalf("首次注册内置命令失败: %v", err)
	}
	if err := registerDefaultCommands(cli); !errors.Is(err, console.ErrDuplicateCommand) {
		t.Fatalf("重复注册应返回 ErrDuplicateCommand，实际为 %v", err)
	}
}
