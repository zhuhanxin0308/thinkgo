package driver

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"thinkgo/framework/log"
)

type consoleFailingWriter struct {
	err error
}

func (w *consoleFailingWriter) Write(data []byte) (int, error) {
	return 0, w.err
}

type consoleShortWriter struct{}

func (w *consoleShortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

// TestConsoleUsesSafeDeterministicFormatting 验证控制台与文件日志共享脱敏和防注入语义。
func TestConsoleUsesSafeDeterministicFormatting(t *testing.T) {
	var output bytes.Buffer
	driver, err := NewConsoleWithWriter(&output)
	if err != nil {
		t.Fatalf("创建控制台驱动失败: %v", err)
	}
	entry := &log.LogEntry{
		Time:    time.Date(2026, 7, 11, 10, 30, 0, 0, time.Local),
		Level:   "info",
		Caller:  "handler.go:12\nforged",
		Message: "hello\r\n[ERROR] forged",
		Context: map[string]interface{}{
			"authorization": "Bearer console-secret",
			"request_id":    "request-123",
		},
	}
	if err := driver.WriteEntry(entry); err != nil {
		t.Fatalf("写控制台日志失败: %v", err)
	}

	formatted := output.String()
	if !strings.Contains(formatted, colorGreen) || !strings.Contains(formatted, colorReset) {
		t.Fatalf("info 日志应包含绿色和重置 ANSI 标记，实际为 %q", formatted)
	}
	if strings.Contains(formatted, "console-secret") || !strings.Contains(formatted, "[REDACTED]") {
		t.Fatalf("控制台日志应深层脱敏，实际为 %q", formatted)
	}
	if strings.Count(formatted, "\n") != 1 || !strings.Contains(formatted, `\n`) || !strings.Contains(formatted, `\r`) {
		t.Fatalf("控制台日志应转义内容控制字符并仅保留结尾换行，实际为 %q", formatted)
	}
	if !strings.Contains(formatted, "request-123") {
		t.Fatalf("控制台日志应保留非敏感上下文，实际为 %q", formatted)
	}
}

// TestConsoleValidatesWriterAndEntries 验证无效输出目标、nil 条目和底层 I/O 错误均被传播。
func TestConsoleValidatesWriterAndEntries(t *testing.T) {
	if _, err := NewConsoleWithWriter(nil); err == nil {
		t.Fatal("nil 控制台输出目标应返回错误")
	}
	var typedNil *bytes.Buffer
	if _, err := NewConsoleWithWriter(typedNil); err == nil {
		t.Fatal("类型化 nil 控制台输出目标应返回错误")
	}
	if err := (&Console{}).WriteEntry(&log.LogEntry{}); err == nil {
		t.Fatal("零值控制台驱动应返回输出目标错误而非 panic")
	}
	var nilConsole *Console
	if err := nilConsole.WriteEntry(&log.LogEntry{}); err == nil {
		t.Fatal("nil 控制台驱动应返回错误而非 panic")
	}
	driver, err := NewConsoleWithWriter(io.Discard)
	if err != nil {
		t.Fatalf("创建控制台驱动失败: %v", err)
	}
	if err := driver.WriteEntry(nil); err == nil {
		t.Fatal("控制台驱动应拒绝 nil 条目")
	}
	if err := driver.SaveEntries([]*log.LogEntry{nil}); err == nil {
		t.Fatal("控制台批量写应拒绝 nil 条目")
	}

	writeErr := errors.New("console write failed")
	failing, err := NewConsoleWithWriter(&consoleFailingWriter{err: writeErr})
	if err != nil {
		t.Fatalf("创建失败输出驱动失败: %v", err)
	}
	entry := &log.LogEntry{Time: time.Now(), Level: "error", Message: "failure"}
	if err := failing.WriteEntry(entry); !errors.Is(err, writeErr) {
		t.Fatalf("控制台驱动应传播输出错误，实际为 %v", err)
	}
	short, err := NewConsoleWithWriter(&consoleShortWriter{})
	if err != nil {
		t.Fatalf("创建短写输出驱动失败: %v", err)
	}
	if err := short.WriteEntry(entry); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("控制台驱动应识别短写，实际为 %v", err)
	}
}

// TestConsoleLevelColorsAndBatchWrite 验证全部级别颜色映射、批量写和关闭路径。
func TestConsoleLevelColorsAndBatchWrite(t *testing.T) {
	testCases := map[string]string{
		"emergency": colorRed,
		"alert":     colorRed,
		"critical":  colorRed,
		"error":     colorRed,
		"warning":   colorYellow,
		"notice":    colorYellow,
		"info":      colorGreen,
		"debug":     colorCyan,
		"sql":       colorBlue,
		"custom":    colorReset,
	}
	for level, expected := range testCases {
		driver := NewConsole()
		if actual := driver.levelColor(level); actual != expected {
			t.Fatalf("级别 %s 的颜色应为 %q，实际为 %q", level, expected, actual)
		}
	}

	var output bytes.Buffer
	driver, err := NewConsoleWithWriter(&output)
	if err != nil {
		t.Fatalf("创建控制台驱动失败: %v", err)
	}
	entries := []*log.LogEntry{
		{Time: time.Now(), Level: "info", Message: "first"},
		{Time: time.Now(), Level: "error", Message: "second"},
	}
	if err := driver.SaveEntries(entries); err != nil {
		t.Fatalf("批量写控制台失败: %v", err)
	}
	if strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("批量写应产生两行日志，实际为 %q", output.String())
	}
	if err := driver.Close(); err != nil {
		t.Fatalf("关闭控制台驱动失败: %v", err)
	}
}

// TestConsoleConcurrentWritesStayWhole 验证并发写入不会交错破坏单行边界。
func TestConsoleConcurrentWritesStayWhole(t *testing.T) {
	var output bytes.Buffer
	driver, err := NewConsoleWithWriter(&output)
	if err != nil {
		t.Fatalf("创建控制台驱动失败: %v", err)
	}
	var waitGroup sync.WaitGroup
	for index := 0; index < 64; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := driver.WriteEntry(&log.LogEntry{Time: time.Now(), Level: "info", Message: "concurrent"}); err != nil {
				t.Errorf("并发写控制台失败: %v", err)
			}
		}()
	}
	waitGroup.Wait()
	if strings.Count(output.String(), "\n") != 64 {
		t.Fatalf("64 次并发写应产生 64 个完整日志行，实际为 %d", strings.Count(output.String(), "\n"))
	}
}
