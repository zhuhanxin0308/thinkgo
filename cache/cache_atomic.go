package cache

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrCacheAtomicUpdateUnsupported 表示当前驱动无法提供单键原子读改写。
	ErrCacheAtomicUpdateUnsupported = errors.New("缓存驱动不支持原子更新")
	// ErrNilCacheUpdate 表示原子更新没有提供计算回调。
	ErrNilCacheUpdate = errors.New("缓存原子更新回调为空")
	// ErrInvalidCachePrefix 表示按前缀清理收到空值或非法字符串。
	ErrInvalidCachePrefix = errors.New("缓存清理前缀非法")
	// ErrCacheNamespaceClearUnsupported 表示带前缀的命名空间无法安全局部清理。
	ErrCacheNamespaceClearUnsupported = errors.New("缓存驱动不支持命名空间清理")
	// ErrCorruptCacheEnvelope 表示内部 fencing 信封损坏。
	ErrCorruptCacheEnvelope = errors.New("缓存代际信封损坏")
	// ErrCacheFenceUnavailable 表示驱动无法提供有效的单调 fencing 代际。
	ErrCacheFenceUnavailable = errors.New("缓存 fencing 代际不可用")
)

// Update 原子读取并改写单个缓存键。
func (c *Cache) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.UpdateContext(context.Background(), key, ttl, update)
}

// UpdateContext 在驱动级原子边界内执行回调，并与标签失效共享同一代际锁。
func (c *Cache) UpdateContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if update == nil {
		return ErrNilCacheUpdate
	}
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	resolvedTTL, err := c.resolveTTL([]time.Duration{ttl})
	if err != nil {
		return err
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	_, contextual := driver.(ContextualAtomicUpdater)
	_, basic := driver.(AtomicUpdater)
	if !contextual && !basic {
		return ErrCacheAtomicUpdateUnsupported
	}
	c.trace("UPDATE", key)
	runUpdate := func(cleanMetadata bool) (bool, error) {
		removed := false
		committedOrigin := uint64(0)
		generation, _, err := c.withFreshAtomicFence(ctx, driver, key, func(generation, started, invalidation uint64) error {
			wrapped := func(raw interface{}, found bool) (interface{}, bool, error) {
				value, found, origin, decodeErr := decodeAtomicCacheValue(raw, found, generation, started, invalidation)
				if decodeErr != nil {
					return nil, false, decodeErr
				}
				next, remove, updateErr := update(value, found)
				if updateErr == nil {
					removed, committedOrigin = remove, origin
					if !remove && generation > 0 {
						next = newAtomicCacheValueEnvelope(generation, origin, next)
					}
				}
				return next, remove, updateErr
			}
			if contextualUpdater, ok := driver.(ContextualAtomicUpdater); ok {
				return contextualUpdater.UpdateContext(ctx, key, resolvedTTL, wrapped)
			}
			return driver.(AtomicUpdater).Update(key, resolvedTTL, wrapped)
		})
		if err != nil {
			return false, err
		}
		if generation > 0 {
			current, fenceErr := c.invalidationForKey(ctx, driver, key)
			if fenceErr != nil {
				return false, fenceErr
			}
			if current > committedOrigin {
				if !removed {
					fenceErr = c.deleteExactGenerationContext(context.WithoutCancel(ctx), driver, key, generation)
				}
				return false, errors.Join(ErrCacheLockLost, fenceErr)
			}
		}
		if !removed || !cleanMetadata {
			return removed, nil
		}
		_, cleanupErr := c.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
		return removed, cleanupErr
	}
	tags, err := c.reverseTagsLockedContext(ctx, driver, key)
	if err != nil {
		return err
	}
	if len(tags) > 0 {
		return c.withTagMutationLockContext(ctx, driver, func() error {
			c.state.tagMu.Lock()
			defer c.state.tagMu.Unlock()
			if repairErr := c.repairMissingKeyMetadataLockedContext(ctx, driver, key); repairErr != nil {
				return repairErr
			}
			_, updateErr := runUpdate(true)
			return updateErr
		})
	}
	removed, err := runUpdate(false)
	if err != nil || !removed {
		return err
	}
	// 删除提交后再做双重检查；若并发新值已经成功写入，不得拆除其标签关系。
	_, _, err = c.getCacheValueAndRepairContext(ctx, driver, key)
	return err
}

// ClearPrefix 只清理当前 store 中属于指定逻辑前缀的键。
func (c *Cache) ClearPrefix(prefix string) error {
	return c.ClearPrefixContext(context.Background(), prefix)
}

// ClearPrefixContext 与普通写入共享标签代际锁，避免清理越过并发成功写入。
func (c *Cache) ClearPrefixContext(ctx context.Context, prefix string) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if prefix == "" {
		return ErrInvalidCachePrefix
	}
	if err := validatePublicCacheKey(prefix); err != nil {
		return errors.Join(ErrInvalidCachePrefix, err)
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	_, contextual := driver.(ContextualPrefixClearer)
	_, basic := driver.(PrefixClearer)
	if !contextual && !basic {
		return ErrCacheNamespaceClearUnsupported
	}
	c.trace("CLEAR_PREFIX", prefix)
	return c.withTagMutationLockContext(ctx, driver, func() error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		return c.clearAtFence(ctx, driver, prefix)
	})
}
