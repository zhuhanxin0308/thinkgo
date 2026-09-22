package console

import (
	"bytes"
	"errors"
	"testing"
)

// TestBaseCommandExecuteReportsMissingImplementation 验证基础命令通过可识别错误
// 报告缺失实现，不向输出流伪装成普通业务消息。
func TestBaseCommandExecuteReportsMissingImplementation(t *testing.T) {
	command := &Command{Signature: "app:missing"}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	output := NewOutputWithWriters(stdout, stderr, false)

	err := command.Execute(NewInput(), output)
	if !errors.Is(err, ErrCommandNotImplemented) {
		t.Fatalf("基础 Execute 应返回 ErrCommandNotImplemented，实际为 %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 || output.Err() != nil {
		t.Fatalf("未实现错误不应被伪装成普通输出: stdout=%q stderr=%q err=%v", stdout, stderr, output.Err())
	}
}

// TestCommandDefinitionBuildersAreIdempotentAndDefensive 验证重复配置不会累加
// 完全相同的声明，且调用方修改返回切片不会污染命令内部定义。
func TestCommandDefinitionBuildersAreIdempotentAndDefensive(t *testing.T) {
	command := &Command{}
	command.AddOption("port", "p", "监听端口", "8080")
	command.AddOption("port", "p", "监听端口", "8080")
	command.AddBoolOption("force", "f", "强制执行")
	command.AddBoolOption("force", "f", "强制执行")
	command.AddArgument("name", "目标名称", true)
	command.AddArgument("name", "目标名称", true)

	options := command.GetOptionDefinitions()
	arguments := command.GetArgumentDefinitions()
	if len(options) != 2 || len(arguments) != 1 {
		t.Fatalf("重复配置不应累加声明: options=%v arguments=%v", options, arguments)
	}
	options[0].Name = "changed"
	arguments[0].Name = "changed"
	if command.GetOptionDefinitions()[0].Name != "port" || command.GetArgumentDefinitions()[0].Name != "name" {
		t.Fatal("定义访问器泄漏了内部切片")
	}
}
