package driver

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/log"
)

// ANSI 颜色常量
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m" // ERROR / CRITICAL / EMERGENCY
	colorYellow = "\033[33m" // WARNING / NOTICE
	colorGreen  = "\033[32m" // INFO
	colorCyan   = "\033[36m" // DEBUG
	colorBlue   = "\033[34m" // SQL
	colorGray   = "\033[90m" // 附加信息（时间、调用位置）
)

// Console 控制台日志驱动
// 在调试模式下输出带 ANSI 颜色的日志到标准输出
type Console struct {
	out *os.File // 输出目标（默认 os.Stdout）
}

// NewConsole 创建控制台日志驱动
func NewConsole() *Console {
	return &Console{out: os.Stdout}
}

// WriteEntry 立即写入单条日志到控制台
func (d *Console) WriteEntry(entry *log.LogEntry) error {
	color := d.levelColor(entry.Level)
	timeStr := entry.Time.Format("15:04:05.000")

	// 格式: [时间] 级别 调用位置 消息 上下文
	var sb strings.Builder
	sb.WriteString(colorGray + "[" + timeStr + "] " + colorReset)
	sb.WriteString(color + fmt.Sprintf("%-7s", strings.ToUpper(entry.Level)) + colorReset + " ")

	if entry.Caller != "" {
		sb.WriteString(colorGray + entry.Caller + " " + colorReset)
	}

	sb.WriteString(entry.Message)

	if sanitizedContext := entry.SanitizedContext(); len(sanitizedContext) > 0 {
		sb.WriteString(colorGray + " | ")
		for k, v := range sanitizedContext {
			sb.WriteString(fmt.Sprintf("%s=%v ", k, v))
		}
		sb.WriteString(colorReset)
	}

	sb.WriteString("\n")
	_, err := d.out.WriteString(sb.String())
	return err
}

// SaveEntries 批量输出日志到控制台
func (d *Console) SaveEntries(entries []*log.LogEntry) error {
	for _, entry := range entries {
		if err := d.WriteEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// Close 关闭控制台驱动（无需释放资源）
func (d *Console) Close() error {
	return nil
}

// levelColor 返回日志级别对应的 ANSI 颜色
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
