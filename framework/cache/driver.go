package cache

import (
	"context"
	"time"
)

// Driver 定义缓存后端必须实现的显式错误与命中语义。
type Driver interface {
	// Get 返回值、命中标志和后端错误；命中标志允许正确缓存 nil。
	Get(key string) (value interface{}, found bool, err error)

	// Set 写入缓存；ttl 为 0 表示永不过期。
	Set(key string, value interface{}, ttl time.Duration) error

	// Has 判断未过期键是否存在。
	Has(key string) (bool, error)

	// Delete 删除指定键，不存在视为成功。
	Delete(key string) error

	// Clear 清空当前驱动管理的数据；除显式授权的整库操作外不得释放缓存锁。
	Clear() error

	// Inc 原子或在驱动能力范围内安全递增整数值。
	Inc(key string, step int64) (int64, error)

	// Dec 原子或在驱动能力范围内安全递减整数值。
	Dec(key string, step int64) (int64, error)
}

// ContextualGetter 为支持请求取消的缓存驱动提供可选读取能力。
type ContextualGetter interface {
	GetContext(ctx context.Context, key string) (value interface{}, found bool, err error)
}

// ContextualSetter 为支持请求取消的缓存驱动提供可选写入能力。
type ContextualSetter interface {
	SetContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error
}

// ContextualHasser 为支持请求取消的缓存驱动提供可选存在性检查能力。
type ContextualHasser interface {
	HasContext(ctx context.Context, key string) (bool, error)
}

// ContextualDeleter 为支持请求取消的缓存驱动提供可选删除能力。
type ContextualDeleter interface {
	DeleteContext(ctx context.Context, key string) error
}

// ContextualClearer 为支持请求取消的缓存驱动提供可选清空能力。
type ContextualClearer interface {
	ClearContext(ctx context.Context) error
}

// ContextualDistributedLocker 为支持请求取消的分布式锁驱动提供可选上下文能力。
type ContextualDistributedLocker interface {
	AcquireLockContext(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)
	ReleaseLockContext(ctx context.Context, key string, owner string) (bool, error)
	RenewLockContext(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)
}

// BatchGetter 定义批量读取缓存的可选能力，结果只包含命中的业务键。
type BatchGetter interface {
	GetMany(keys []string) (map[string]interface{}, error)
}

// ContextualBatchGetter 为支持请求取消的批量读取驱动提供可选能力。
type ContextualBatchGetter interface {
	GetManyContext(ctx context.Context, keys []string) (map[string]interface{}, error)
}

// BatchSetter 定义批量写入缓存的可选能力，调用方不应依赖批量写入的事务原子性。
type BatchSetter interface {
	SetMany(values map[string]interface{}, ttl time.Duration) error
}

// ContextualBatchSetter 为支持请求取消的批量写入驱动提供可选能力。
type ContextualBatchSetter interface {
	SetManyContext(ctx context.Context, values map[string]interface{}, ttl time.Duration) error
}
