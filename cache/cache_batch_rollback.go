package cache

import (
	"context"
	"errors"
	"time"
)

type cacheBatchSnapshot struct {
	key      string
	value    interface{}
	found    bool
	expiry   time.Time
	captured bool
}

// rollbackCacheBatchSnapshot 只恢复仍由失败批次拥有的值，其他请求的新代际或删除均保持不变。
func rollbackCacheBatchSnapshot(ctx context.Context, updater ExpiryAtomicUpdater, previous cacheBatchSnapshot, generation uint64) error {
	err := updater.UpdateWithExpiryContext(ctx, previous.key, 0, func(raw interface{}, found bool, _ time.Time) (interface{}, bool, *time.Time, error) {
		if !found {
			return nil, false, nil, errCacheFenceNoChange
		}
		_, current, enveloped, err := decodeCacheValueEnvelope(raw)
		if err != nil {
			return nil, false, nil, err
		}
		if !enveloped || current != generation {
			return nil, false, nil, errCacheFenceNoChange
		}
		if !previous.found {
			return nil, true, nil, nil
		}
		_, _, previousEnveloped, err := decodeCacheValueEnvelope(previous.value)
		if err != nil {
			return nil, false, nil, err
		}
		value := previous.value
		if !previousEnveloped {
			// 旧格式值也必须受批次期间或之后的失效水位保护，不能借恢复复活已失效数据。
			value = newCacheValueEnvelope(generation, value)
		}
		return value, false, &previous.expiry, nil
	})
	if errors.Is(err, errCacheFenceNoChange) {
		return nil
	}
	return err
}
