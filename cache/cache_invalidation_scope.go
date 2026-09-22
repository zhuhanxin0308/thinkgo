package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const (
	// 每个精确桶与前缀桶各自有数量和字节预算；精确桶最多 64 个，不按分桶扩大失效范围。
	cacheInvalidationBuckets      = 64
	maxCacheInvalidationScopes    = 256
	maxCacheInvalidationBytes     = 256 << 10
	keyInvalidationScopePrefix    = "key:"
	prefixInvalidationScopePrefix = "prefix:"
)

// ErrCacheInvalidationCapacity 表示失效范围元数据达到安全边界，调用方可先执行全 store Flush 回收。
var ErrCacheInvalidationCapacity = errors.New("缓存失效范围容量已满")

func keyInvalidationScope(key string) string {
	digest := sha256.Sum256([]byte(key))
	return keyInvalidationScopePrefix + hex.EncodeToString(digest[:])
}

func (c *Cache) scopedInvalidationKey() string {
	return cacheFencePrefix + c.storeName + ":prefixes"
}

func keyInvalidationBucket(key string) int {
	digest := sha256.Sum256([]byte(key))
	return int(digest[0]) % cacheInvalidationBuckets
}

func (c *Cache) keyInvalidationBucketKey(bucket int) string {
	return cacheFencePrefix + c.storeName + ":keys:" + strconv.Itoa(bucket)
}

// boundedScopeWatermarks 同时限制数量与 JSON 转义后的字节数，给文件后端的外层信封保留空间。
func boundedScopeWatermarks(watermarks map[string]interface{}) (interface{}, bool, error) {
	if len(watermarks) > maxCacheInvalidationScopes {
		return nil, false, ErrCacheInvalidationCapacity
	}
	encoded, err := json.Marshal(watermarks)
	if err != nil {
		return nil, false, err
	}
	// 预留 uint64 最长十进制代际长度，已有 scope 换代不会因为多一位数字失去容量。
	reservedBytes := len(encoded)
	for _, value := range watermarks {
		generation, err := parseFenceGenerationValue(value)
		if err != nil {
			return nil, false, err
		}
		reservedBytes += len("18446744073709551615") - len(strconv.FormatUint(generation, 10))
	}
	if reservedBytes > maxCacheInvalidationBytes {
		return nil, false, ErrCacheInvalidationCapacity
	}
	return watermarks, false, nil
}

// beginKeyInvalidation 只发布实际被删除键的水位，商品失效不会误伤会话等其他业务键。
func (c *Cache) beginKeyInvalidation(ctx context.Context, driver Driver, keys []string) (uint64, error) {
	generation, err := c.nextFenceGenerationContext(ctx, driver)
	if err != nil {
		return 0, err
	}
	groups := [cacheInvalidationBuckets][]string{}
	for _, key := range keys {
		bucket := keyInvalidationBucket(key)
		groups[bucket] = append(groups[bucket], key)
	}
	// 调用方持有标签变更锁；先预检所有桶，避免可预见容量不足造成半途发布。
	for bucket, keys := range groups {
		if len(keys) == 0 {
			continue
		}
		raw, found, err := getCacheValueContext(ctx, driver, c.keyInvalidationBucketKey(bucket))
		if err != nil {
			return 0, err
		}
		if _, _, err := keyScopeWatermarks(raw, found, keys, generation); err != nil {
			return 0, err
		}
	}
	for bucket, keys := range groups {
		if len(keys) == 0 {
			continue
		}
		err = atomicUpdateCacheValueContext(ctx, driver, c.keyInvalidationBucketKey(bucket), 0, func(raw interface{}, found bool) (interface{}, bool, error) {
			return keyScopeWatermarks(raw, found, keys, generation)
		})
		if err != nil {
			return 0, err
		}
	}
	return generation, nil
}

func keyScopeWatermarks(raw interface{}, found bool, keys []string, generation uint64) (interface{}, bool, error) {
	watermarks, err := scopeWatermarks(raw, found)
	if err != nil {
		return nil, false, err
	}
	for _, key := range keys {
		scope := keyInvalidationScope(key)
		current, _ := parseFenceGenerationValue(watermarks[scope])
		if current < generation {
			watermarks[scope] = strconv.FormatUint(generation, 10)
		}
	}
	return boundedScopeWatermarks(watermarks)
}

// scopeWatermarks 使用后端可以无损存储的字符串代际，读取时拒绝损坏的范围元数据。
func scopeWatermarks(raw interface{}, found bool) (map[string]interface{}, error) {
	if !found {
		return make(map[string]interface{}), nil
	}
	values, ok := raw.(map[string]interface{})
	if !ok || len(values) > maxCacheInvalidationScopes {
		return nil, ErrCacheFenceUnavailable
	}
	result := make(map[string]interface{}, len(values))
	for scope, value := range values {
		keyScope := strings.HasPrefix(scope, keyInvalidationScopePrefix) && len(scope) == len(keyInvalidationScopePrefix)+sha256.Size*2
		prefixScope := strings.HasPrefix(scope, prefixInvalidationScopePrefix) && len(scope) > len(prefixInvalidationScopePrefix)
		if !keyScope && !prefixScope {
			return nil, ErrCacheFenceUnavailable
		}
		if _, err := parseFenceGenerationValue(value); err != nil {
			return nil, err
		}
		result[scope] = value
	}
	return result, nil
}

func (c *Cache) beginPrefixInvalidation(ctx context.Context, driver Driver, prefix string) (uint64, error) {
	if prefix == "" {
		generation, err := c.beginFenceInvalidationContext(ctx, driver)
		if err != nil {
			return 0, err
		}
		return generation, c.compactScopesAtFence(ctx, driver, generation)
	}
	generation, err := c.nextFenceGenerationContext(ctx, driver)
	if err != nil {
		return 0, err
	}
	err = atomicUpdateCacheValueContext(ctx, driver, c.scopedInvalidationKey(), 0, func(raw interface{}, found bool) (interface{}, bool, error) {
		watermarks, err := scopeWatermarks(raw, found)
		if err != nil {
			return nil, false, err
		}
		for existing, value := range watermarks {
			if !strings.HasPrefix(existing, prefixInvalidationScopePrefix) {
				continue
			}
			logicalPrefix := strings.TrimPrefix(existing, prefixInvalidationScopePrefix)
			current, _ := parseFenceGenerationValue(value)
			if strings.HasPrefix(logicalPrefix, prefix) && current <= generation {
				delete(watermarks, existing)
			}
			if strings.HasPrefix(prefix, logicalPrefix) && current >= generation {
				return boundedScopeWatermarks(watermarks)
			}
		}
		watermarks[prefixInvalidationScopePrefix+prefix] = strconv.FormatUint(generation, 10)
		return boundedScopeWatermarks(watermarks)
	})
	return generation, err
}

// compactScopesAtFence 仅删除已被整 store 水位覆盖的旧范围，保留更晚并发发布的范围。
func (c *Cache) compactScopesAtFence(ctx context.Context, driver Driver, fence uint64) error {
	keys := []string{c.scopedInvalidationKey()}
	for bucket := 0; bucket < cacheInvalidationBuckets; bucket++ {
		keys = append(keys, c.keyInvalidationBucketKey(bucket))
	}
	for _, key := range keys {
		if err := c.compactScopeBucketAtFence(ctx, driver, key, fence); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) compactScopeBucketAtFence(ctx context.Context, driver Driver, key string, fence uint64) error {
	err := atomicUpdateCacheValueContext(ctx, driver, key, 0, func(raw interface{}, found bool) (interface{}, bool, error) {
		if !found {
			return nil, false, errCacheFenceNoChange
		}
		watermarks, err := scopeWatermarks(raw, found)
		if err != nil {
			return nil, false, err
		}
		for scope, raw := range watermarks {
			generation, _ := parseFenceGenerationValue(raw)
			if generation <= fence {
				delete(watermarks, scope)
			}
		}
		return boundedScopeWatermarks(watermarks)
	})
	if errors.Is(err, errCacheFenceNoChange) {
		return nil
	}
	return err
}

// invalidationForKey 合并整 store、当前键与匹配前缀的水位，其他键的失效不参与判定。
func (c *Cache) invalidationForKey(ctx context.Context, driver Driver, key string) (uint64, error) {
	maximum := uint64(0)
	raw, found, err := getCacheValueContext(ctx, driver, c.keyInvalidationBucketKey(keyInvalidationBucket(key)))
	if err != nil {
		return 0, err
	}
	if found {
		watermarks, valid := raw.(map[string]interface{})
		if !valid {
			return 0, ErrCacheFenceUnavailable
		}
		if value, exists := watermarks[keyInvalidationScope(key)]; exists {
			generation, err := parseFenceGenerationValue(value)
			if err != nil {
				return 0, err
			}
			maximum = max(maximum, generation)
		}
	}
	raw, found, err = getCacheValueContext(ctx, driver, c.scopedInvalidationKey())
	if err != nil {
		return 0, err
	}
	watermarks, err := scopeWatermarks(raw, found)
	if err != nil {
		return 0, err
	}
	for scope, value := range watermarks {
		if strings.HasPrefix(scope, prefixInvalidationScopePrefix) && strings.HasPrefix(key, strings.TrimPrefix(scope, prefixInvalidationScopePrefix)) {
			generation, _ := parseFenceGenerationValue(value)
			maximum = max(maximum, generation)
		}
	}
	// 先读取范围，再读取全局水位；Flush 先发布全局水位再压缩范围，读取不能落入两者之间的空窗。
	global, err := c.currentInvalidationGeneration(ctx, driver)
	return max(maximum, global), err
}

// visibleCacheValue 在读取边界屏蔽迟到旧写者，不能依赖仍可能暂停的写者自行回滚。
func (c *Cache) visibleCacheValue(ctx context.Context, driver Driver, key string, raw interface{}) (interface{}, bool, error) {
	value, generation, _, err := decodeCacheValueAtOrigin(raw)
	if err != nil {
		return nil, false, err
	}
	if !cacheDriverSupportsAtomicFencing(driver) {
		return value, true, nil
	}
	invalidation, err := c.invalidationForKey(ctx, driver, key)
	if err != nil {
		return nil, false, err
	}
	if invalidation > 0 && generation <= invalidation {
		return nil, false, nil
	}
	return value, true, nil
}

func (c *Cache) getVisibleCacheValue(ctx context.Context, driver Driver, key string) (interface{}, bool, error) {
	raw, found, err := getCacheValueContext(ctx, driver, key)
	if err != nil || !found {
		return nil, false, err
	}
	return c.visibleCacheValue(ctx, driver, key, raw)
}

// clearAtFence 在发布失效水位后逐键条件删除；开始于清理之后的新写入不受影响。
func (c *Cache) clearAtFence(ctx context.Context, driver Driver, prefix string) error {
	if !cacheDriverSupportsAtomicFencing(driver) {
		if prefix == "" {
			return clearCacheDriverContext(ctx, driver)
		}
		if contextual, ok := driver.(ContextualPrefixClearer); ok {
			return contextual.ClearPrefixContext(ctx, prefix)
		}
		if clearer, ok := driver.(PrefixClearer); ok {
			return clearer.ClearPrefix(prefix)
		}
		return ErrCacheNamespaceClearUnsupported
	}
	clearer, ok := driver.(ConditionalPrefixClearer)
	if !ok {
		return ErrCacheNamespaceClearUnsupported
	}
	if validator, ok := driver.(PrefixClearValidator); ok {
		if err := validator.ValidateClearPrefix(prefix); err != nil {
			return err
		}
	}
	fence, err := c.beginPrefixInvalidation(ctx, driver, prefix)
	if err != nil {
		return err
	}
	return clearer.ClearPrefixIfContext(ctx, prefix, nil, func(raw interface{}) (bool, error) {
		_, generation, _, err := decodeCacheValueAtOrigin(raw)
		if err != nil {
			return false, errors.Join(ErrCorruptCacheEnvelope, err)
		}
		return generation <= fence, nil
	})
}
