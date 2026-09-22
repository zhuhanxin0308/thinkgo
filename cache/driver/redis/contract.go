package redis

import (
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/contract"
)

const cacheFenceMetadataPrefix = contract.FenceMetadataPrefix

// 后端错误与中立契约共享身份，调用方可统一使用 errors.Is。
var (
	ErrInvalidRedisConfig   = contract.ErrInvalidRedisConfig
	ErrInvalidRedisClient   = contract.ErrInvalidRedisClient
	ErrInvalidRedisContext  = contract.ErrInvalidRedisContext
	ErrUnsafeRedisFlush     = contract.ErrUnsafeRedisFlush
	ErrInvalidCacheLock     = contract.ErrInvalidCacheLock
	ErrCacheBatchTooLarge   = contract.ErrCacheBatchTooLarge
	ErrCorruptCacheEntry    = contract.ErrCorruptCacheEntry
	ErrInvalidDriverTTL     = contract.ErrInvalidDriverTTL
	ErrInvalidCounterStep   = contract.ErrInvalidCounterStep
	ErrNilAtomicUpdate      = contract.ErrNilAtomicUpdate
	ErrAtomicUpdateConflict = contract.ErrAtomicUpdateConflict
	ErrInvalidCachePrefix   = contract.ErrInvalidCachePrefix
)

func validateDriverTTL(ttl time.Duration) error  { return contract.ValidateDriverTTL(ttl) }
func containsControlCharacter(value string) bool { return contract.ContainsControlCharacter(value) }
func validateCounterStep(step int64) error       { return contract.ValidateCounterStep(step) }
