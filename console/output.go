package console

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
)

// Output 管理命令标准输出、错误输出、颜色和首个写入错误。
type Output struct {
	mu     sync.Mutex
	stdout io.Writer
	stderr io.Writer
	color  bool
	err    error
}

// NewOutput 创建绑定当前进程标准流的输出器；仅在交互式终端启用颜色。
func NewOutput() *Output {
	return NewOutputWithAutoColor(os.Stdout, os.Stderr)
}

// NewOutputWithAutoColor 创建绑定指定标准流的输出器，并按 stdout 是否为
// 交互式终端自动决定颜色；管道、文件和缓冲区默认不输出 ANSI 序列。
func NewOutputWithAutoColor(stdout, stderr io.Writer) *Output {
	return NewOutputWithWriters(stdout, stderr, terminalColorEnabled(stdout))
}

// NewOutputWithWriters 创建可测试、可嵌入的输出器。
func NewOutputWithWriters(stdout, stderr io.Writer, color bool) *Output {
	output := &Output{stdout: stdout, stderr: stderr, color: color}
	if stdout == nil || stderr == nil {
		output.err = ErrInvalidOutput
		if stdout == nil {
			output.stdout = io.Discard
		}
		if stderr == nil {
			output.stderr = io.Discard
		}
	}
	return output
}

// Write 写入标准输出。
func (o *Output) Write(message string) {
	o.write(false, message)
}

// Writeln 写入标准输出并换行。
func (o *Output) Writeln(message string) {
	o.write(false, message+"\n")
}

// Info 写入绿色信息；非交互输出不附加 ANSI 序列。
func (o *Output) Info(message string) {
	o.writeStatus(false, "32", message)
}

// Error 写入红色错误信息到标准错误流。
func (o *Output) Error(message string) {
	o.writeStatus(true, "31", message)
}

// Warning 写入黄色警告信息。
func (o *Output) Warning(message string) {
	o.writeStatus(false, "33", message)
}

// Success 写入绿色成功信息。
func (o *Output) Success(message string) {
	o.writeStatus(false, "32", message)
}

// Err 返回首个输出写入错误，供 CLI 入口生成非零退出码。
func (o *Output) Err() error {
	if o == nil {
		return ErrInvalidOutput
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func (o *Output) writeStatus(toStderr bool, colorCode, message string) {
	message = escapeTerminalControls(message)
	if o != nil && o.color {
		message = "\x1b[" + colorCode + "m" + message + "\x1b[0m"
	}
	o.write(toStderr, message+"\n")
}

func (o *Output) write(toStderr bool, message string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return
	}
	target := o.stdout
	if toStderr {
		target = o.stderr
	}
	if target == nil {
		o.err = ErrInvalidOutput
		return
	}
	_, o.err = io.WriteString(target, message)
}

// escapeTerminalControls 转义状态消息中的控制字符，防止换行伪造和 ANSI 终端注入。
func escapeTerminalControls(message string) string {
	var escaped strings.Builder
	for _, current := range message {
		if !unicode.IsControl(current) {
			escaped.WriteRune(current)
			continue
		}
		switch current {
		case '\n':
			escaped.WriteString(`\n`)
		case '\r':
			escaped.WriteString(`\r`)
		case '\t':
			escaped.WriteString(`\t`)
		default:
			if current <= 0xff {
				_, _ = fmt.Fprintf(&escaped, "\\x%02x", current)
			} else {
				_, _ = fmt.Fprintf(&escaped, "\\u{%x}", current)
			}
		}
	}
	return escaped.String()
}

func terminalColorEnabled(writer io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
