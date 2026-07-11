package console

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestBaseCommandExecuteReportsMissingImplementation 验证基础命令不会静默吞掉未实现的 Execute。
func TestBaseCommandExecuteReportsMissingImplementation(t *testing.T) {
	cmd := &Command{Signature: "app:missing"}
	output := &Output{}
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建 stdout 捕获管道失败: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = writePipe
	defer func() {
		os.Stdout = originalStdout
	}()

	defer func() {
		_ = writePipe.Close()
		if recovered := recover(); recovered != nil {
			t.Fatalf("基础 Execute 应明确输出错误而不是 panic: %v", recovered)
		}
		content, err := io.ReadAll(readPipe)
		if err != nil {
			t.Fatalf("读取 stdout 捕获内容失败: %v", err)
		}
		if !strings.Contains(string(content), "missing implementation") {
			t.Fatalf("基础 Execute 应输出未实现错误，实际为 %q", string(content))
		}
	}()

	cmd.Execute(NewInput(), output)
}
