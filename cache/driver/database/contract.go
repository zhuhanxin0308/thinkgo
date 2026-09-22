package database

import (
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/contract"
)

// 数据库缓存复用中立错误与精确计数规则，不依赖其他缓存后端。
var (
	ErrInvalidCacheDatabase = contract.ErrInvalidCacheDatabase
	ErrInvalidCacheTable    = contract.ErrInvalidCacheTable
	ErrCacheKeyTooLong      = contract.ErrCacheKeyTooLong
	ErrInvalidCacheLock     = contract.ErrInvalidCacheLock
	ErrCacheLockBusy        = contract.ErrCacheLockBusy
	ErrCacheLockLost        = contract.ErrCacheLockLost
	ErrCorruptCacheEntry    = contract.ErrCorruptCacheEntry
	ErrCounterOverflow      = contract.ErrCounterOverflow
	ErrInvalidCounterValue  = contract.ErrInvalidCounterValue
	ErrInvalidCounterStep   = contract.ErrInvalidCounterStep
	ErrInvalidDriverTTL     = contract.ErrInvalidDriverTTL
)

func validateDriverTTL(ttl time.Duration) error           { return contract.ValidateDriverTTL(ttl) }
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
