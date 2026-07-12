package driver

import (
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"

	"thinkgo/framework/log"
)

// ANSI 颜色常量。
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m" // ERROR / CRITICAL / EMERGENCY
	colorYellow = "\033[33m" // WARNING / NOTICE
	colorGreen  = "\033[32m" // INFO
	colorCyan   = "\033[36m" // DEBUG
	colorBlue   = "\033[34m" // SQL
)

// Console 控制台日志驱动；复用 LogEntry 的统一脱敏、防注入格式化。
type Console struct {
	writer io.Writer
	mu     sync.Mutex
}

// NewConsole 创建输出到标准输出的控制台日志驱动。
func NewConsole() *Console {
	return &Console{writer: os.Stdout}
}

// NewConsoleWithWriter 创建可测试、可嵌入的控制台驱动。
func NewConsoleWithWriter(writer io.Writer) (*Console, error) {
	if isNilWriter(writer) {
		return nil, errors.New("控制台日志输出目标不能为空")
	}
	return &Console{writer: writer}, nil
}

// WriteEntry 以完整单行写入日志，避免并发输出交错。
func (d *Console) WriteEntry(entry *log.LogEntry) error {
	if err := d.validate(); err != nil {
		return err
	}
	if entry == nil {
		return errors.New("日志条目不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeEntryLocked(entry)
}

// SaveEntries 在同一临界区批量输出，保证批次中的日志连续。
func (d *Console) SaveEntries(entries []*log.LogEntry) error {
	if err := d.validate(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, entry := range entries {
		if entry == nil {
			return errors.New("日志条目不能为空")
		}
		if err := d.writeEntryLocked(entry); err != nil {
			return err
		}
	}
	return nil
}

func (d *Console) writeEntryLocked(entry *log.LogEntry) error {
	line := d.levelColor(entry.Level) + entry.FormatEntry() + colorReset + "\n"
	written, err := io.WriteString(d.writer, line)
	if err != nil {
		return err
	}
	if written != len(line) {
		return io.ErrShortWrite
	}
	return nil
}

func (d *Console) validate() error {
	if d == nil || isNilWriter(d.writer) {
		return errors.New("控制台日志输出目标不能为空")
	}
	return nil
}

func isNilWriter(writer io.Writer) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Close 不关闭外部注入的输出目标，其生命周期仍由调用方管理。
func (d *Console) Close() error {
	return nil
}

// levelColor 返回日志级别对应的 ANSI 颜色。
func (d *Console) levelColor(level string) string {
	switch strings.ToLower(level) {
	case "emergency", "alert", "critical", "error":
		return colorRed
	case "warning", "notice":
		return colorYellow
	case "info":
		return colorGreen
	case "debug":
		return colorCyan
	case "sql":
		return colorBlue
	default:
		return colorReset
	}
}
