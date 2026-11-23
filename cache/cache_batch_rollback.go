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
		// 按原样恢复，使裸值仍按代际零判断可见性，旧信封也保留原来的来源和提交代际。
		return previous.value, false, &previous.expiry, nil
	})
	if errors.Is(err, errCacheFenceNoChange) {
		return nil
	}
	return err
}
