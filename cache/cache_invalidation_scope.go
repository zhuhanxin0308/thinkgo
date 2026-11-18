package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

const (
	// 每个精确桶与前缀桶各自有数量和字节预算；精确桶最多 64 个，总槽位覆盖多标签并发清理。
	cacheInvalidationBuckets      = 64
	maxCacheInvalidationScopes    = 2048
	maxCacheInvalidationBytes     = 512 << 10
	keyInvalidationScopePrefix    = "key:"
	prefixInvalidationScopePrefix = "prefix:"
	completedScopeWatermarkPrefix = "done:"
)

// ErrCacheInvalidationCapacity 表示失效范围元数据达到安全边界，调用方可先执行全 store Flush 回收。
var ErrCacheInvalidationCapacity = errors.New("缓存失效范围容量已满")

func keyInvalidationScope(key string) string {
	digest := sha256.Sum256([]byte(key))
	return keyInvalidationScopePrefix + hex.EncodeToString(digest[:])
}

func (c *Cache) scopedInvalidationKey() string {
	return cacheFencePrefix + cacheMetadataScope + ":prefixes"
}

func keyInvalidationBucket(key string) int {
	digest := sha256.Sum256([]byte(key))
	return int(digest[0]) % cacheInvalidationBuckets
}

func (c *Cache) keyInvalidationBucketKey(bucket int) string {
	return cacheFencePrefix + cacheMetadataScope + ":keys:" + strconv.Itoa(bucket)
}

func parseScopeWatermark(raw interface{}) (generation uint64, completed bool, err error) {
	if text, ok := raw.(string); ok {
		if rest, isDone := strings.CutPrefix(text, completedScopeWatermarkPrefix); isDone {
			gen, parseErr := parseFenceGenerationValue(rest)
			return gen, parseErr == nil, parseErr
		}
	}
	gen, err := parseFenceGenerationValue(raw)
	return gen, false, err
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
	// 预留 uint64 最长十进制代际（及精确键完成态前缀）长度，已有 scope 换代或标记完成不会失去容量。
	reservedBytes := len(encoded)
	for scope, value := range watermarks {
		generation, completed, err := parseScopeWatermark(value)
		if err != nil {
			return nil, false, err
		}
		digits := strconv.FormatUint(generation, 10)
		if strings.HasPrefix(scope, keyInvalidationScopePrefix) {
			if completed {
				digits = completedScopeWatermarkPrefix + digits
			}
			reservedBytes += len(completedScopeWatermarkPrefix+"18446744073709551615") - len(digits)
		} else {
			reservedBytes += len("18446744073709551615") - len(digits)
		}
	}
	if reservedBytes > maxCacheInvalidationBytes {
		return nil, false, ErrCacheInvalidationCapacity
	}
	return watermarks, false, nil
}

// beginKeyInvalidation 只发布实际被删除键的水位，商品失效不会误伤会话等其他业务键。
func (c *Cache) beginKeyInvalidation(ctx context.Context, driver Driver, keys []string) (uint64, error) {
	groups := [cacheInvalidationBuckets][]string{}
	for _, key := range keys {
		bucket := keyInvalidationBucket(key)
		groups[bucket] = append(groups[bucket], key)
	}
	floor, err := c.currentInvalidationGeneration(ctx, driver)
	if err != nil {
		return 0, err
	}
	if rawPrefixes, found, prefixErr := getCacheValueContext(ctx, driver, c.scopedInvalidationKey()); prefixErr != nil {
		return 0, prefixErr
	} else if found {
		prefixWatermarks, parseErr := scopeWatermarks(rawPrefixes, true)
		if parseErr != nil {
			return 0, parseErr
		}
		for _, value := range prefixWatermarks {
			gen, _, _ := parseScopeWatermark(value)
			floor = max(floor, gen)
		}
	}
	for _, key := range keys {
		if rawVal, found, readErr := getCacheValueContext(ctx, driver, key); readErr != nil {
			return 0, readErr
		} else if found {
			if _, currentGen, enveloped, decErr := decodeCacheValueEnvelope(rawVal); decErr != nil {
				return 0, decErr
			} else if enveloped {
				floor = max(floor, currentGen)
			}
		}
	}
	var bucketSnapshots [cacheInvalidationBuckets]struct {
		raw   interface{}
		found bool
	}
	for bucket, bucketKeys := range groups {
		if len(bucketKeys) == 0 {
			continue
		}
		raw, found, readErr := getCacheValueContext(ctx, driver, c.keyInvalidationBucketKey(bucket))
		if readErr != nil {
			return 0, readErr
		}
		bucketSnapshots[bucket].raw = raw
		bucketSnapshots[bucket].found = found
		if found {
			existing, parseErr := scopeWatermarks(raw, true)
			if parseErr != nil {
				return 0, parseErr
			}
			for _, value := range existing {
				gen, _, _ := parseScopeWatermark(value)
				floor = max(floor, gen)
			}
		}
	}
	generation, err := c.allocateFenceGenerationAboveContext(ctx, driver, floor)
	if err != nil {
		return 0, err
	}
	// 调用方持有标签变更锁；先预检所有桶，避免可预见容量不足造成半途发布。
	for bucket, bucketKeys := range groups {
		if len(bucketKeys) == 0 {
			continue
		}
		if _, _, err := keyScopeWatermarks(bucketSnapshots[bucket].raw, bucketSnapshots[bucket].found, bucketKeys, generation); err != nil {
			return 0, err
		}
	}
	for bucket, bucketKeys := range groups {
		if len(bucketKeys) == 0 {
			continue
		}
		err = atomicUpdateCacheValueContext(ctx, driver, c.keyInvalidationBucketKey(bucket), 0, func(raw interface{}, found bool) (interface{}, bool, error) {
			return keyScopeWatermarks(raw, found, bucketKeys, generation)
		})
		if err != nil {
			return 0, err
		}
	}
	return generation, nil
}

// completeKeyInvalidation 将已完成物理删除的精确键水位标记为可按代数裁剪，避免长期 Forget 泄漏桶槽位。
func (c *Cache) completeKeyInvalidation(ctx context.Context, driver Driver, keys []string, fence uint64) error {
	if fence == 0 || len(keys) == 0 {
		return nil
	}
	groups := [cacheInvalidationBuckets][]string{}
	for _, key := range keys {
		bucket := keyInvalidationBucket(key)
		groups[bucket] = append(groups[bucket], key)
	}
	doneText := completedScopeWatermarkPrefix + strconv.FormatUint(fence, 10)
	for bucket, bucketKeys := range groups {
		if len(bucketKeys) == 0 {
			continue
		}
		err := atomicUpdateCacheValueContext(ctx, driver, c.keyInvalidationBucketKey(bucket), 0, func(raw interface{}, found bool) (interface{}, bool, error) {
			if !found {
				return nil, false, errCacheFenceNoChange
			}
			watermarks, err := scopeWatermarks(raw, found)
			if err != nil {
				return nil, false, err
			}
			changed := false
			for _, key := range bucketKeys {
				scope := keyInvalidationScope(key)
				existing, exists := watermarks[scope]
				if !exists {
					continue
				}
				currentGen, completed, parseErr := parseScopeWatermark(existing)
				if parseErr != nil {
					return nil, false, parseErr
				}
				if !completed && currentGen == fence {
					watermarks[scope] = doneText
					changed = true
				}
			}
			if !changed {
				return nil, false, errCacheFenceNoChange
			}
			return boundedScopeWatermarks(watermarks)
		})
		if err != nil && !errors.Is(err, errCacheFenceNoChange) {
			return err
		}
	}
	return nil
}

type reclaimableKeyScope struct {
	scope      string
	generation uint64
}

func keyScopeWatermarks(raw interface{}, found bool, keys []string, generation uint64) (interface{}, bool, error) {
	watermarks, err := scopeWatermarks(raw, found)
	if err != nil {
		return nil, false, err
	}
	activeBatch := make(map[string]bool, len(keys))
	generationText := strconv.FormatUint(generation, 10)
	for _, key := range keys {
		scope := keyInvalidationScope(key)
		activeBatch[scope] = true
		current, _, _ := parseScopeWatermark(watermarks[scope])
		if current <= generation {
			watermarks[scope] = generationText
		}
	}
	if len(watermarks) <= maxCacheInvalidationScopes {
		if result, remove, checkErr := boundedScopeWatermarks(watermarks); checkErr == nil {
			return result, remove, nil
		} else if !errors.Is(checkErr, ErrCacheInvalidationCapacity) {
			return nil, false, checkErr
		}
	}
	reclaimable := make([]reclaimableKeyScope, 0, len(watermarks))
	for scope, value := range watermarks {
		if activeBatch[scope] || !strings.HasPrefix(scope, keyInvalidationScopePrefix) {
			continue
		}
		scopeGen, completed, parseErr := parseScopeWatermark(value)
		if parseErr == nil && completed && scopeGen < generation {
			reclaimable = append(reclaimable, reclaimableKeyScope{scope: scope, generation: scopeGen})
		}
	}
	sort.Slice(reclaimable, func(i, j int) bool {
		if reclaimable[i].generation != reclaimable[j].generation {
			return reclaimable[i].generation < reclaimable[j].generation
		}
		return reclaimable[i].scope < reclaimable[j].scope
	})
	evicted := 0
	if excess := len(watermarks) - maxCacheInvalidationScopes; excess > 0 {
		if excess > len(reclaimable) {
			return nil, false, ErrCacheInvalidationCapacity
		}
		for ; evicted < excess; evicted++ {
			delete(watermarks, reclaimable[evicted].scope)
		}
	}
	for {
		result, remove, checkErr := boundedScopeWatermarks(watermarks)
		if checkErr == nil {
			return result, remove, nil
		}
		if !errors.Is(checkErr, ErrCacheInvalidationCapacity) || evicted >= len(reclaimable) {
			return nil, false, checkErr
		}
		delete(watermarks, reclaimable[evicted].scope)
		evicted++
	}
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
		if prefixScope {
			if _, err := parseFenceGenerationValue(value); err != nil {
				return nil, err
			}
		} else {
			if _, _, err := parseScopeWatermark(value); err != nil {
				return nil, err
			}
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
	floor, err := c.currentInvalidationGeneration(ctx, driver)
	if err != nil {
		return 0, err
	}
	if rawScoped, found, readErr := getCacheValueContext(ctx, driver, c.scopedInvalidationKey()); readErr != nil {
		return 0, readErr
	} else if found {
		existing, parseErr := scopeWatermarks(rawScoped, true)
		if parseErr != nil {
			return 0, parseErr
		}
		for _, value := range existing {
			gen, _, _ := parseScopeWatermark(value)
			floor = max(floor, gen)
		}
	}
	generation, err := c.allocateFenceGenerationAboveContext(ctx, driver, floor)
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
			generation, _, _ := parseScopeWatermark(raw)
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
	raw, found, err := getCacheValueContext(ctx, driver, c.keyInvalidationBucketKey(keyInvalidationBucket(key)))
	if err != nil {
		return 0, err
	}
	exact, err := exactKeyInvalidation(raw, found, key)
	if err != nil {
		return 0, err
	}
	raw, found, err = getCacheValueContext(ctx, driver, c.scopedInvalidationKey())
	if err != nil {
		return 0, err
	}
	watermarks, err := scopeWatermarks(raw, found)
	if err != nil {
		return 0, err
	}
	// 先读取范围，再读取全局水位；Flush 先发布全局水位再压缩范围，读取不能落入两者之间的空窗。
	global, err := c.currentInvalidationGeneration(ctx, driver)
	return max(exact, matchingPrefixInvalidation(watermarks, key), global), err
}

func exactKeyInvalidation(raw interface{}, found bool, key string) (uint64, error) {
	if !found {
		return 0, nil
	}
	watermarks, valid := raw.(map[string]interface{})
	if !valid {
		return 0, ErrCacheFenceUnavailable
	}
	if value, exists := watermarks[keyInvalidationScope(key)]; exists {
		generation, _, err := parseScopeWatermark(value)
		return generation, err
	}
	return 0, nil
}

func matchingPrefixInvalidation(watermarks map[string]interface{}, key string) uint64 {
	var maximum uint64
	for scope, value := range watermarks {
		if prefix, isPrefix := strings.CutPrefix(scope, prefixInvalidationScopePrefix); isPrefix && strings.HasPrefix(key, prefix) {
			generation, _ := parseFenceGenerationValue(value)
			maximum = max(maximum, generation)
		}
	}
	return maximum
}

// visibleCacheValue 在读取边界屏蔽迟到旧写者，不能依赖仍可能暂停的写者自行回滚。
func (c *Cache) visibleCacheValue(ctx context.Context, driver Driver, key string, raw interface{}) (interface{}, bool, error) {
	return visibleCacheValueAtFence(raw, func() (uint64, error) {
		if !cacheDriverSupportsAtomicFencing(driver) {
			return 0, nil
		}
		return c.invalidationForKey(ctx, driver, key)
	})
}

func visibleCacheValueAtFence(raw interface{}, invalidationForKey func() (uint64, error)) (interface{}, bool, error) {
	value, generation, _, err := decodeCacheValueAtOrigin(raw)
	if err != nil {
		return nil, false, err
	}
	invalidation, err := invalidationForKey()
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
