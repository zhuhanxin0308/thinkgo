package cache

import "time"

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
