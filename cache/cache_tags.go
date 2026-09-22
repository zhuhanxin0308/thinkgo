package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// TaggedCache 提供带标签的缓存读写能力。
type TaggedCache struct {
	cache *Cache
	tags  []string
}

// Get 读取带标签缓存中的指定键。
func (c *TaggedCache) Get(key string) (interface{}, bool, error) {
	return c.GetContext(context.Background(), key)
}

// GetContext 读取带标签缓存，并在未命中时使用相同上下文清理元数据。
func (c *TaggedCache) GetContext(ctx context.Context, key string) (interface{}, bool, error) {
	if c == nil || c.cache == nil {
		return nil, false, ErrCacheDriverNotConfigured
	}
	if ctx == nil {
		return nil, false, ErrInvalidCacheContext
	}
	return c.cache.GetContext(ctx, key)
}

// Set 写入缓存并维护标签与键的双向元数据；省略 ttl 时使用 store 默认值。
func (c *TaggedCache) Set(key string, value interface{}, ttl ...time.Duration) error {
	return c.SetContext(context.Background(), key, value, ttl...)
}

// SetContext 写入带标签缓存，并让标签元数据和驱动操作响应请求取消。
func (c *TaggedCache) SetContext(ctx context.Context, key string, value interface{}, ttl ...time.Duration) error {
	if c == nil || c.cache == nil {
		return ErrCacheDriverNotConfigured
	}
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	resolvedTTL, err := c.cache.resolveTTL(ttl)
	if err != nil {
		return err
	}
	driver, release, err := c.cache.driver()
	if err != nil {
		return err
	}
	defer release()
	c.cache.trace("TAG_SET", key)
	return c.cache.withTagMutationLockContext(ctx, driver, func() error {
		c.cache.state.tagMu.Lock()
		defer c.cache.state.tagMu.Unlock()
		if err := c.cache.repairMissingKeyMetadataLockedContext(ctx, driver, key); err != nil {
			return err
		}
		rollback, err := c.bindKeyToTagsLockedContext(ctx, driver, key)
		if err != nil {
			return err
		}
		if err = c.cache.writeFencedCacheValueContext(ctx, driver, key, value, resolvedTTL); err != nil {
			return errors.Join(err, rollback())
		}
		return nil
	})
}

// Has 判断带标签缓存是否存在。
func (c *TaggedCache) Has(key string) (bool, error) {
	return c.HasContext(context.Background(), key)
}

// HasContext 检查带标签缓存是否存在，并在未命中时清理元数据。
func (c *TaggedCache) HasContext(ctx context.Context, key string) (bool, error) {
	if c == nil || c.cache == nil {
		return false, ErrCacheDriverNotConfigured
	}
	if ctx == nil {
		return false, ErrInvalidCacheContext
	}
	return c.cache.HasContext(ctx, key)
}

// Forget 删除带标签缓存，并同步移除全部标签映射。
func (c *TaggedCache) Forget(key string) error {
	return c.ForgetContext(context.Background(), key)
}

// ForgetContext 删除带标签缓存并清理全部反向关系。
func (c *TaggedCache) ForgetContext(ctx context.Context, key string) error {
	if c == nil || c.cache == nil {
		return ErrCacheDriverNotConfigured
	}
	return c.cache.ForgetContext(ctx, key)
}

// Flush 清理标签关联的全部缓存键及其反向元数据。
func (c *TaggedCache) Flush() error {
	return c.FlushContext(context.Background())
}

// FlushContext 清理标签关联的缓存键，并在驱动支持时响应请求取消。
func (c *TaggedCache) FlushContext(ctx context.Context) error {
	if c == nil || c.cache == nil {
		return ErrCacheDriverNotConfigured
	}
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	driver, release, err := c.cache.driver()
	if err != nil {
		return err
	}
	defer release()
	c.cache.trace("TAG_FLUSH", strings.Join(c.tags, ","))
	return c.cache.withTagMutationLockContext(ctx, driver, func() error {
		c.cache.state.tagMu.Lock()
		defer c.cache.state.tagMu.Unlock()
		keys := make(map[string][]string)
		for _, tag := range c.tags {
			members, memberErr := c.cache.tagMembersLockedContext(ctx, driver, tag)
			if memberErr != nil {
				return memberErr
			}
			for _, key := range members {
				keys[key] = append(keys[key], tag)
			}
		}
		fence := uint64(0)
		if cacheDriverSupportsAtomicFencing(driver) {
			members := make([]string, 0, len(keys))
			for key := range keys {
				members = append(members, key)
			}
			fence, err = c.cache.beginKeyInvalidation(ctx, driver, members)
			if err != nil {
				return err
			}
		}
		liveKeys := make(map[string]bool, len(keys))
		forwardGhosts := make(map[string][]string)
		preservedKeys := make(map[string]bool)
		// 变更前区分已过期幽灵成员和仍存在的业务值；只有后者要求完整反向关系。
		for key, requiredTags := range keys {
			raw, found, valueErr := getCacheValueContext(ctx, driver, key)
			if valueErr != nil {
				return valueErr
			}
			if !found {
				continue
			}
			if fence > 0 {
				_, generation, _, decodeErr := decodeCacheValueAtOrigin(raw)
				if decodeErr != nil {
					return decodeErr
				}
				if generation > fence {
					preservedKeys[key] = true
					continue
				}
			}
			reverse, reverseErr := c.cache.reverseTagsLockedContext(ctx, driver, key)
			if reverseErr != nil {
				return reverseErr
			}
			consistent := true
			for _, tag := range requiredTags {
				if !containsString(reverse, tag) {
					consistent = false
				}
			}
			if consistent {
				liveKeys[key] = true
			} else {
				forwardGhosts[key] = requiredTags
			}
		}
		resultErr := error(nil)
		for key, requiredTags := range keys {
			if liveKeys[key] || preservedKeys[key] || len(forwardGhosts[key]) > 0 {
				continue
			}
			_, cleanupErr := c.cache.removeKeyFromTagsLockedContext(ctx, driver, key, requiredTags)
			resultErr = errors.Join(resultErr, cleanupErr)
		}
		for key, tags := range forwardGhosts {
			_, cleanupErr := c.cache.removeForwardMembersLockedContext(ctx, driver, key, tags)
			resultErr = errors.Join(resultErr, cleanupErr)
		}
		for key := range liveKeys {
			deleted, deleteErr := c.cache.deleteAtFenceContext(ctx, driver, key, fence)
			if deleteErr != nil {
				resultErr = errors.Join(resultErr, deleteErr)
				continue
			}
			if !deleted {
				continue
			}
			_, unbindErr := c.cache.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
			resultErr = errors.Join(resultErr, unbindErr)
		}
		return resultErr
	})
}

// removeForwardMembersLockedContext 仅移除缺少反向证明的正向成员，绝不删除仍存在的业务值。
func (c *Cache) removeForwardMembersLockedContext(ctx context.Context, driver Driver, key string, tags []string) (func() error, error) {
	updates := make([]metadataUpdate, 0, len(tags))
	for _, tag := range tags {
		members, err := c.tagMembersLockedContext(ctx, driver, tag)
		if err != nil {
			return nil, err
		}
		if !containsString(members, key) {
			continue
		}
		updates = append(updates, metadataUpdate{
			key:    c.tagMetaKey(tag),
			before: cloneStrings(members),
			after:  removeString(members, key),
		})
	}
	return applyMetadataUpdatesContext(ctx, driver, updates)
}

func (c *TaggedCache) bindKeyToTagsLockedContext(ctx context.Context, driver Driver, key string) (func() error, error) {
	reverse, err := c.cache.reverseTagsLockedContext(ctx, driver, key)
	if err != nil {
		return nil, err
	}
	finalReverse := append([]string(nil), reverse...)
	for _, tag := range c.tags {
		if !containsString(finalReverse, tag) {
			finalReverse = append(finalReverse, tag)
		}
	}
	if len(finalReverse) > maxTagsPerCacheKey {
		return nil, fmt.Errorf("%w: 单键标签超过 %d", ErrInvalidCacheTag, maxTagsPerCacheKey)
	}

	updates := make([]metadataUpdate, 0, len(c.tags)+1)
	checkedTags := make(map[string]bool, len(finalReverse))
	for _, tag := range finalReverse {
		members, memberErr := c.cache.tagMembersLockedContext(ctx, driver, tag)
		if memberErr != nil {
			return nil, memberErr
		}
		containsKey := containsString(members, key)
		if !containsKey {
			if len(members) >= maxTagMembers {
				return nil, fmt.Errorf("%w: %s 成员超过 %d", ErrInvalidCacheTag, tag, maxTagMembers)
			}
			updates = append(updates, metadataUpdate{
				key:    c.cache.tagMetaKey(tag),
				before: cloneStrings(members),
				after:  append(cloneStrings(members), key),
			})
		}
		checkedTags[tag] = true
	}
	if len(checkedTags) != len(finalReverse) {
		return nil, ErrCorruptTagMetadata
	}
	if len(finalReverse) != len(reverse) {
		updates = append(updates, metadataUpdate{
			key:    c.cache.tagReverseKey(key),
			before: cloneStrings(reverse),
			after:  cloneStrings(finalReverse),
		})
	}
	return applyMetadataUpdatesContext(ctx, driver, updates)
}

func (c *Cache) unbindKeyFromAllTagsLockedContext(ctx context.Context, driver Driver, key string) (func() error, error) {
	return c.removeKeyFromTagsLockedContext(ctx, driver, key, nil)
}

// removeKeyFromTagsLockedContext 同时修复单边映射；requiredTags 用于清理只有正向关系的幽灵成员。
func (c *Cache) removeKeyFromTagsLockedContext(ctx context.Context, driver Driver, key string, requiredTags []string) (func() error, error) {
	reverseTags, err := c.reverseTagsLockedContext(ctx, driver, key)
	if err != nil {
		return nil, err
	}
	tags := cloneStrings(reverseTags)
	for _, tag := range requiredTags {
		if !containsString(tags, tag) {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		return noopMetadataRollback, nil
	}
	updates := make([]metadataUpdate, 0, len(tags)+1)
	for _, tag := range tags {
		members, memberErr := c.tagMembersLockedContext(ctx, driver, tag)
		if memberErr != nil {
			return nil, memberErr
		}
		if !containsString(members, key) {
			continue
		}
		filtered := removeString(members, key)
		updates = append(updates, metadataUpdate{
			key:    c.tagMetaKey(tag),
			before: cloneStrings(members),
			after:  cloneStrings(filtered),
		})
	}
	if len(reverseTags) > 0 {
		updates = append(updates, metadataUpdate{
			key:    c.tagReverseKey(key),
			before: cloneStrings(reverseTags),
			after:  nil,
		})
	}
	return applyMetadataUpdatesContext(ctx, driver, updates)
}

func (c *Cache) tagMembersLockedContext(ctx context.Context, driver Driver, tag string) ([]string, error) {
	return readStringMetadataContext(ctx, driver, c.tagMetaKey(tag), maxTagMembers, validatePublicCacheKey)
}

func (c *Cache) reverseTagsLockedContext(ctx context.Context, driver Driver, key string) ([]string, error) {
	return readStringMetadataContext(ctx, driver, c.tagReverseKey(key), maxTagsPerCacheKey, validateCacheTag)
}

func readStringMetadataContext(ctx context.Context, driver Driver, key string, limit int, validator func(string) error) ([]string, error) {
	raw, found, err := getCacheValueContext(ctx, driver, key)
	if err != nil || !found {
		return nil, err
	}
	result := make([]string, 0)
	switch typed := raw.(type) {
	case []string:
		result = append(result, typed...)
	case []interface{}:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, ErrCorruptTagMetadata
			}
			result = append(result, text)
		}
	default:
		return nil, ErrCorruptTagMetadata
	}
	if len(result) > limit || len(result) == 0 {
		return nil, ErrCorruptTagMetadata
	}
	seen := make(map[string]bool, len(result))
	filtered := make([]string, 0, len(result))
	for _, value := range result {
		if value == "" || seen[value] || validator(value) != nil {
			return nil, ErrCorruptTagMetadata
		}
		seen[value] = true
		filtered = append(filtered, value)
	}
	return filtered, nil
}

type metadataUpdate struct {
	key    string
	before []string
	after  []string
}

// applyMetadataUpdates 顺序提交元数据，并在任何错误时反向恢复所有可能已写入的键。
func applyMetadataUpdatesContext(ctx context.Context, driver Driver, updates []metadataUpdate) (func() error, error) {
	for index, update := range updates {
		if err := writeStringMetadataContext(ctx, driver, update.key, update.after); err != nil {
			rollback := metadataRollbackContext(context.WithoutCancel(ctx), driver, updates[:index+1])
			return nil, errors.Join(err, rollback())
		}
	}
	return metadataRollbackContext(context.WithoutCancel(ctx), driver, updates), nil
}

func metadataRollbackContext(ctx context.Context, driver Driver, updates []metadataUpdate) func() error {
	return func() error {
		var resultErr error
		for index := len(updates) - 1; index >= 0; index-- {
			resultErr = errors.Join(resultErr, writeStringMetadataContext(ctx, driver, updates[index].key, updates[index].before))
		}
		return resultErr
	}
}

func writeStringMetadataContext(ctx context.Context, driver Driver, key string, values []string) error {
	if len(values) == 0 {
		return deleteCacheValueContext(ctx, driver, key)
	}
	return setCacheValueContext(ctx, driver, key, values, 0)
}

func noopMetadataRollback() error {
	return nil
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func (c *Cache) tagMetaKey(tag string) string {
	return tagMetaPrefix + c.storeName + ":" + tag
}

func (c *Cache) tagReverseKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return tagReversePrefix + c.storeName + ":" + hex.EncodeToString(digest[:])
}

func validateCacheTag(tag string) error {
	if tag == "" || len(tag) > maxCacheTagBytes || !utf8.ValidString(tag) || hasControlCharacter(tag) || strings.TrimSpace(tag) != tag {
		return fmt.Errorf("%w: %q", ErrInvalidCacheTag, tag)
	}
	return nil
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func removeString(items []string, target string) []string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if item != target {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
