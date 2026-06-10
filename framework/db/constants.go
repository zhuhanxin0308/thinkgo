package db

// ==================== 查询常量 ====================

const (
	// DefaultTimeFormat 默认时间格式（MySQL datetime）
	DefaultTimeFormat = "2006-01-02 15:04:05"
	// DefaultDateFormat 默认日期格式（MySQL date）
	DefaultDateFormat = "2006-01-02"
	// DefaultPageSize 默认分页大小
	DefaultPageSize = 20
	// TimestampValueTypeUnix 表示自动时间戳写入 Unix 秒级时间戳（对应 ThinkPHP 的 int 类型）。
	TimestampValueTypeUnix = "unix"
	// TimestampValueTypeTimestamp 表示自动时间戳写入 MySQL TIMESTAMP 格式（实际与 datetime 相同）。
	TimestampValueTypeTimestamp = "timestamp"
	// TimestampValueTypeDateTime 表示自动时间戳写入 datetime 格式字符串。
	TimestampValueTypeDateTime = "datetime"
	// TimestampValueTypeDate 表示自动时间戳写入 date 格式字符串。
	TimestampValueTypeDate = "date"
)
