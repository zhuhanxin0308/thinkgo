package cache

import "time"

// Driver interface for cache drivers
type Driver interface {
	// Get gets a value from cache
	Get(key string) interface{}

	// Set sets a value in cache
	Set(key string, val interface{}, ttl time.Duration)

	// Has checks if key exists
	Has(key string) bool

	// Delete deletes a key
	Delete(key string)

	// Clear clears the cache
	Clear()

	// Inc increments a key
	Inc(key string, step int64) int64

	// Dec decrements a key
	Dec(key string, step int64) int64
}

// noopDriver 在缓存驱动缺失时兜底，避免运行时因 nil 驱动 panic。
type noopDriver struct{}

func (noopDriver) Get(key string) interface{} {
	return nil
}

func (noopDriver) Set(key string, val interface{}, ttl time.Duration) {}

func (noopDriver) Has(key string) bool {
	return false
}

func (noopDriver) Delete(key string) {}

func (noopDriver) Clear() {}

func (noopDriver) Inc(key string, step int64) int64 {
	return 0
}

func (noopDriver) Dec(key string, step int64) int64 {
	return 0
}
