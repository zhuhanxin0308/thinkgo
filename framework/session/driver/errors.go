package driver

import "errors"

const (
	maxSessionIDBytes         = 128
	maxFileSessionEntryBytes  = 1 << 20
	fileSessionLockShardCount = 64
)

var (
	// ErrInvalidSessionPath 表示文件 Session 根目录为空、不可访问或不是目录。
	ErrInvalidSessionPath = errors.New("会话存储路径非法")
	// ErrInvalidSessionID 表示 Session ID 不符合受限字符集或长度要求。
	ErrInvalidSessionID = errors.New("会话 ID 非法")
	// ErrUnsafeSessionFile 表示受管路径是符号链接、目录或发生文件身份替换。
	ErrUnsafeSessionFile = errors.New("会话文件不安全")
	// ErrSessionEntryTooLarge 表示单个 Session 文件超过驱动硬上限。
	ErrSessionEntryTooLarge = errors.New("会话文件超过大小上限")
	// ErrSessionLockTimeout 表示跨进程原子更新未能在限时内取得锁。
	ErrSessionLockTimeout = errors.New("会话文件锁超时")
	// ErrInvalidSessionUpdate 表示原子更新回调为空。
	ErrInvalidSessionUpdate = errors.New("会话原子更新回调非法")
	// ErrInvalidMemoryCapacity 表示内存 Session 容量配置为负数。
	ErrInvalidMemoryCapacity = errors.New("内存会话容量非法")
	// ErrMemoryCapacityExhausted 表示内存 Session 已达到容量上限且无法继续写入。
	ErrMemoryCapacityExhausted = errors.New("内存会话容量已耗尽")
)

// validateSessionID 统一约束所有驱动入口，避免存储键注入与资源滥用。
func validateSessionID(id string) error {
	if id == "" || len(id) > maxSessionIDBytes {
		return ErrInvalidSessionID
	}
	for _, character := range id {
		isDigit := character >= '0' && character <= '9'
		isLower := character >= 'a' && character <= 'z'
		isUpper := character >= 'A' && character <= 'Z'
		if isDigit || isLower || isUpper || character == '-' || character == '_' {
			continue
		}
		return ErrInvalidSessionID
	}
	return nil
}
