package driver

import (
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/contract"
)

// 驱动兼容名称引用同一组中立错误值，保证 errors.Is 在核心和各后端之间一致。
var (
	ErrInvalidCounterValue     = contract.ErrInvalidCounterValue
	ErrCounterOverflow         = contract.ErrCounterOverflow
	ErrInvalidCounterStep      = contract.ErrInvalidCounterStep
	ErrInvalidDriverTTL        = contract.ErrInvalidDriverTTL
	ErrInvalidCachePath        = contract.ErrInvalidCachePath
	ErrInvalidMemoryCapacity   = contract.ErrInvalidMemoryCapacity
	ErrMemoryCapacityExhausted = contract.ErrMemoryCapacityExhausted
	ErrUnsafeCacheEntry        = contract.ErrUnsafeCacheEntry
	ErrCacheEntryTooLarge      = contract.ErrCacheEntryTooLarge
	ErrCorruptCacheEntry       = contract.ErrCorruptCacheEntry
	ErrInvalidCacheLock        = contract.ErrInvalidCacheLock
	ErrCacheLockBusy           = contract.ErrCacheLockBusy
	ErrCacheLockLost           = contract.ErrCacheLockLost
	ErrInvalidRedisConfig      = contract.ErrInvalidRedisConfig
	ErrInvalidRedisClient      = contract.ErrInvalidRedisClient
	ErrInvalidRedisContext     = contract.ErrInvalidRedisContext
	ErrUnsafeRedisFlush        = contract.ErrUnsafeRedisFlush
	ErrInvalidCacheDatabase    = contract.ErrInvalidCacheDatabase
	ErrInvalidCacheTable       = contract.ErrInvalidCacheTable
	ErrCacheKeyTooLong         = contract.ErrCacheKeyTooLong
	ErrCacheBatchTooLarge      = contract.ErrCacheBatchTooLarge
	ErrNilAtomicUpdate         = contract.ErrNilAtomicUpdate
	ErrAtomicUpdateConflict    = contract.ErrAtomicUpdateConflict
	ErrInvalidCachePrefix      = contract.ErrInvalidCachePrefix
	ErrUnscopedCacheEntry      = contract.ErrUnscopedCacheEntry
)

func validateDriverTTL(ttl time.Duration) error           { return contract.ValidateDriverTTL(ttl) }
func containsControlCharacter(value string) bool          { return contract.ContainsControlCharacter(value) }
func validateCounterStep(step int64) error                { return contract.ValidateCounterStep(step) }
func strictCounterValue(value interface{}) (int64, error) { return contract.StrictCounterValue(value) }
func decodeCounterJSON(encoded []byte) (interface{}, error) {
	return contract.DecodeCounterJSON(encoded)
}
func checkedCounterAdd(value, step int64) (int64, error) {
	return contract.CheckedCounterAdd(value, step)
}
func checkedCounterSubtract(value, step int64) (int64, error) {
	return contract.CheckedCounterSubtract(value, step)
}
