package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/console"
	"thinkgo/framework/db"
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
func TestBuildConsoleAppUsesExplicitCommand(t *testing.T) {
	basePath := t.TempDir()
	writeThinkTestConfig(t, basePath)
	app, err := framework.BuildConsoleApp(basePath)
	if err != nil {
		t.Fatalf("构建命令行应用失败: %v", err)
	}
	defer func() {
		if err := app.Close(); err != nil {
			t.Errorf("关闭命令行应用失败: %v", err)
		}
	}()
	database, databaseErr := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
	manager, managerErr := framework.ResolveServiceAs[*db.Manager](app, framework.ServiceDBManager)
	if databaseErr == nil || database != nil || managerErr != nil || manager == nil {
		t.Fatalf("非 run 命令应保留空管理器但不建立默认连接: DB=%#v dbErr=%v manager=%#v managerErr=%v", database, databaseErr, manager, managerErr)
	}
	if _, err := manager.Default(); err == nil {
		t.Fatal("非 run 命令的空管理器不应返回默认数据库连接")
	}
}

// writeThinkTestConfig 为命令入口测试提供明确的临时应用配置。
func writeThinkTestConfig(t *testing.T, basePath string) {
	t.Helper()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建 think 测试配置目录失败: %v", err)
	}
	configs := map[string]string{
		"app.json":     `{"app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`,
		"log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cache.json":   `{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`,
		"view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"cookie.json":  `{}`,
		"session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入 think 测试配置 %q 失败: %v", name, err)
		}
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
