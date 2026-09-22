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

// ContextualIncrementer 为单调代际和计数提供可选的上下文递增能力。
type ContextualIncrementer interface {
	IncContext(ctx context.Context, key string, step int64) (int64, error)
}

// PrefixClearer 定义只清理指定逻辑前缀的可选能力。
// 实现必须按字面量匹配前缀，且不得删除不属于该前缀的数据或活动锁。
type PrefixClearer interface {
	ClearPrefix(prefix string) error
}

// ContextualPrefixClearer 为支持请求取消的前缀清理驱动提供可选能力。
type ContextualPrefixClearer interface {
	ClearPrefixContext(ctx context.Context, prefix string) error
}

// ConditionalPrefixClearer 在每个键的原子边界内重新读取并判断是否删除。
// 空前缀表示全部业务数据；驱动必须保留锁与 fencing 元数据，且不能把未命中项重新创建。
type ConditionalPrefixClearer interface {
	ClearPrefixIfContext(ctx context.Context, prefix string, match func(string) bool, remove func(interface{}) (bool, error)) error
}

// PrefixClearValidator 在发布失效水位前只读检查目标范围的权限和驱动能力。
type PrefixClearValidator interface {
	ValidateClearPrefix(prefix string) error
}

// AtomicUpdater 定义单键原子读改写能力。
// update 可能在乐观事务冲突时重试，因此回调必须只计算返回值，不得产生外部副作用。
type AtomicUpdater interface {
	Update(key string, ttl time.Duration, update func(value interface{}, found bool) (next interface{}, remove bool, err error)) error
}

// ContextualAtomicUpdater 为支持请求取消的单键原子更新驱动提供可选能力。
type ContextualAtomicUpdater interface {
	UpdateContext(ctx context.Context, key string, ttl time.Duration, update func(value interface{}, found bool) (next interface{}, remove bool, err error)) error
}

// LockGuardedUpdater 在同一原子边界内验证租约 owner 并提交值，阻止迟到持有者覆盖新状态。
// 返回 false 表示租约已丢失且没有提交；回调不得重入驱动，也不得产生外部副作用。
type LockGuardedUpdater interface {
	UpdateIfLockOwnerContext(ctx context.Context, key string, ttl time.Duration, lockKey, owner string, update func(interface{}, bool) (interface{}, bool, error)) (bool, error)
}

// TTLAtomicUpdater 定义保留现有键剩余 TTL 的单键原子读改写能力。
// 键不存在时，新值按永久缓存创建；该接口主要用于计数等不能意外延长生命周期的操作。
type TTLAtomicUpdater interface {
	UpdatePreserveTTL(key string, update func(value interface{}, found bool) (next interface{}, remove bool, err error)) error
}

// ConditionalTTLAtomicUpdater 允许回调根据同一次原子读取决定是否保留物理项的绝对过期时间。
// preserveTTL=false 时新值永久有效；未命中项始终按永久项创建，remove=true 优先删除。
// 回调可能因乐观事务冲突重试，因此不得产生外部副作用；值与 TTL 必须在同一次提交中生效。
type ConditionalTTLAtomicUpdater interface {
	UpdatePreserveTTLConditionally(key string, update func(value interface{}, found bool) (next interface{}, remove bool, preserveTTL bool, err error)) error
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
