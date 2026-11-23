package cache

import "context"

// strictBatchGetter 保留单键读取的坏类型错误，并按参数顺序读取。
type strictBatchGetter interface {
	GetManyStrictContext(context.Context, []string) (map[string]interface{}, error)
}

func (c *Cache) batchInvalidationReader(ctx context.Context, driver Driver, values map[string]interface{}) (func(string) (uint64, error), error) {
	if !cacheDriverSupportsAtomicFencing(driver) {
		return func(string) (uint64, error) { return 0, nil }, nil
	}
	read, err := c.readBatchWatermarks(ctx, driver, values, 0)
	if err != nil {
		return nil, err
	}
	return func(key string) (uint64, error) {
		state, err := read(key)
		return state.highest, err
	}, nil
}

type batchFenceStatus uint8

const (
	batchFenceUnreadable batchFenceStatus = iota
	batchFenceChecked
	batchFenceReused
)

// checkBatchWriteFence 识别重号时需要检查整批，某个键的较高水位不能掩盖另一键的等值水位。
func (c *Cache) checkBatchWriteFence(ctx context.Context, driver Driver, keys []string, values map[string]interface{}, generation uint64) (batchFenceStatus, error) {
	read, err := c.readBatchWatermarks(ctx, driver, values, generation)
	if err != nil {
		return batchFenceUnreadable, err
	}
	var stale error
	for _, key := range keys {
		state, err := read(key)
		if err != nil {
			return batchFenceUnreadable, err
		}
		if state.reused {
			return batchFenceReused, ErrCacheLockLost
		}
		if cacheOriginInvalidated(generation, state.highest) {
			stale = ErrCacheLockLost
		}
	}
	return batchFenceChecked, stale
}

func (c *Cache) readBatchWatermarks(ctx context.Context, driver Driver, values map[string]interface{}, ticket uint64) (func(string) (keyInvalidationState, error), error) {
	batch, ok := driver.(strictBatchGetter)
	if !ok || len(values) == 0 {
		return func(key string) (keyInvalidationState, error) {
			return c.readKeyInvalidation(ctx, driver, key, ticket, ticket)
		}, nil
	}
	var buckets [cacheInvalidationBuckets]bool
	keys := make([]string, 0, min(len(values), cacheInvalidationBuckets)+2)
	for key := range values {
		bucket := keyInvalidationBucket(key)
		if !buckets[bucket] {
			keys = append(keys, c.keyInvalidationBucketKey(bucket))
			buckets[bucket] = true
		}
	}
	prefixKey, globalKey := c.scopedInvalidationKey(), c.fenceInvalidationKey()
	// 全局水位最后读取，以覆盖 Flush 已经压缩的范围。
	fences, err := batch.GetManyStrictContext(ctx, append(keys, prefixKey, globalKey))
	if err != nil {
		return nil, err
	}
	rawPrefixes, found := fences[prefixKey]
	prefixes, err := scopeWatermarks(rawPrefixes, found)
	if err != nil {
		return nil, err
	}
	var global uint64
	if raw, exists := fences[globalKey]; exists {
		global, err = parseFenceGenerationValue(raw)
		if err != nil {
			return nil, err
		}
	}
	return func(key string) (keyInvalidationState, error) {
		raw, exists := fences[c.keyInvalidationBucketKey(keyInvalidationBucket(key))]
		exact, err := exactKeyInvalidation(raw, exists, key)
		return summarizeKeyInvalidation(key, exact, global, prefixes, ticket, ticket), err
	}, nil
}
