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
	batch, ok := driver.(strictBatchGetter)
	if !ok || len(values) == 0 {
		return func(key string) (uint64, error) { return c.invalidationForKey(ctx, driver, key) }, nil
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
	return func(key string) (uint64, error) {
		raw, exists := fences[c.keyInvalidationBucketKey(keyInvalidationBucket(key))]
		exact, err := exactKeyInvalidation(raw, exists, key)
		return max(exact, matchingPrefixInvalidation(prefixes, key), global), err
	}, nil
}
