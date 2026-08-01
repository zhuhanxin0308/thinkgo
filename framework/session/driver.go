package session

import (
	"context"
	"time"
)

// Driver 定义可区分缺失值并支持原子读改写的 Session 存储协议。
// Update 的回调在同一 Session ID 的排他区间内执行；remove=true 表示删除记录。
// 回调必须只计算新值，不得阻塞或重入同一驱动，否则会延长跨进程锁持有时间或造成死锁。
type Driver interface {
	Read(id string) (data string, found bool, err error)
	Write(id string, data string) error
	Delete(id string) error
	Clear() error
	Update(id string, update func(data string, found bool) (next string, remove bool, err error)) error
}

// ContextualReader 为支持请求取消的 Session Driver 提供上下文读取能力。
type ContextualReader interface {
	ReadContext(ctx context.Context, id string) (data string, found bool, err error)
}

// ContextualUpdater 为支持请求取消的 Session Driver 提供上下文原子更新能力。
type ContextualUpdater interface {
	UpdateContext(ctx context.Context, id string, update func(data string, found bool) (next string, remove bool, err error)) error
}

// GarbageCollector 由支持过期回收的驱动实现。
type GarbageCollector interface {
	GC(maxLifetime time.Duration) (int, error)
}
