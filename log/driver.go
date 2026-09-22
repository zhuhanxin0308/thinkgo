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
	Time     time.Time              // 日志时间
	Level    string                 // 日志级别（info/error/warning/debug/sql 等）
	Message  string                 // 日志消息
	Context  map[string]interface{} // 驱动独占的脱敏上下文视图；修改它不改变已冻结的格式化快照。
	Caller   string                 // 调用位置（文件名:行号）
	snapshot *logContextSnapshot
}

// logContextSnapshot 只在入队前生成一次，多个驱动复用不可变的脱敏结果和 JSON 文本。
type logContextSnapshot struct {
	values  map[string]any
	encoded string
}

func newLogEntry(at time.Time, level, message string, fields map[string]any) *LogEntry {
	values := sanitizeLogContext(fields)
	encoded := ""
	if len(values) > 0 {
		data, err := json.Marshal(values)
		if err != nil {
			encoded = `{"log_context":"[UNSERIALIZABLE]"}`
		} else {
			encoded = string(data)
		}
	}
	return &LogEntry{Time: at, Level: level, Message: message, Context: values, snapshot: &logContextSnapshot{values: values, encoded: encoded}}
}

// forDriver 隔离驱动可修改的数据，脱敏不再由驱动重复执行。
func (e *LogEntry) forDriver() *LogEntry {
	if e == nil {
		return nil
	}
	result := *e
	if result.snapshot == nil {
		result.snapshot = newLogEntry(e.Time, e.Level, e.Message, e.Context).snapshot
	}
	result.Context = cloneContext(result.snapshot.values)
	return &result
}

// FormatEntry 将日志条目格式化为字符串
// 格式: [时间][级别][调用位置] 消息 | 上下文JSON
func (e *LogEntry) FormatEntry() string {
	var sb strings.Builder

	// [时间][级别]
	sb.WriteString(fmt.Sprintf("[%s][%s]",
		e.Time.Format("2006-01-02 15:04:05.000"),
		escapeLogLineValue(strings.ToUpper(e.Level))))

	// [调用位置]（如果有）
	if e.Caller != "" {
		sb.WriteString(fmt.Sprintf("[%s]", escapeLogLineValue(e.Caller)))
	}

	// 消息
	sb.WriteString(" " + escapeLogLineValue(e.Message))

	// 上下文（如果有）
	if e.snapshot != nil {
		if e.snapshot.encoded != "" {
			sb.WriteString(" | " + e.snapshot.encoded)
		}
	} else if len(e.Context) > 0 {
		sb.WriteString(" | " + marshalSanitizedContext(e.Context))
	}

	return sb.String()
}

// SanitizedContext 返回脱敏后的上下文副本，避免日志落盘或控制台输出泄露凭据。
func (e *LogEntry) SanitizedContext() map[string]interface{} {
	if e == nil {
		return nil
	}
	if e.snapshot != nil {
		return cloneContext(e.snapshot.values)
	}
	return sanitizeLogContext(e.Context)
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

// LocationAwareDriver 表示能够使用应用时区执行内部日期清理的日志驱动。
type LocationAwareDriver interface {
	SetLocation(location *time.Location)
}
