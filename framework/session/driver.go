package session

import "time"

// Driver 会话存储驱动接口
type Driver interface {
	// Read 读取会话数据
	Read(id string) (string, error)

	// Write 写入会话数据
	Write(id string, data string) error

	// Delete 删除会话数据
	Delete(id string) error

	// Clear 清空所有会话数据
	Clear() error
}

// GarbageCollector 由支持过期回收的驱动可选实现，用于清理过期会话。
type GarbageCollector interface {
	// GC 回收超过 maxLifetime 未更新的会话，返回删除数量。
	GC(maxLifetime time.Duration) (int, error)
}
