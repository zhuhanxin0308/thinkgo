package log

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LogEntry 结构化日志条目
// 对应 ThinkPHP 8 的日志记录结构，包含上下文和调用位置信息
type LogEntry struct {
	Time    time.Time              // 日志时间
	Level   string                 // 日志级别（info/error/warning/debug/sql 等）
	Message string                 // 日志消息
	Context map[string]interface{} // 上下文数据（如请求参数、用户ID等）
	Caller  string                 // 调用位置（文件名:行号）
}

// FormatEntry 将日志条目格式化为字符串
// 格式: [时间][级别][调用位置] 消息 | 上下文JSON
func (e *LogEntry) FormatEntry() string {
	var sb strings.Builder

	// [时间][级别]
	sb.WriteString(fmt.Sprintf("[%s][%s]",
		e.Time.Format("2006-01-02 15:04:05.000"),
		strings.ToUpper(e.Level)))

	// [调用位置]（如果有）
	if e.Caller != "" {
		sb.WriteString(fmt.Sprintf("[%s]", e.Caller))
	}

	// 消息
	sb.WriteString(" " + e.Message)

	// 上下文（如果有）
	if len(e.Context) > 0 {
		ctxBytes, _ := json.Marshal(e.Context)
		sb.WriteString(" | " + string(ctxBytes))
	}

	return sb.String()
}

// Driver 日志驱动接口
// 所有日志驱动必须实现此接口
type Driver interface {
	// SaveEntries 批量保存日志条目
	SaveEntries(entries []*LogEntry) error

	// WriteEntry 立即写入单条日志
	WriteEntry(entry *LogEntry) error

	// Close 关闭驱动，释放资源
	Close() error
}
