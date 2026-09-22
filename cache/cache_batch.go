package cache

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// GetMany 批量读取缓存，默认使用不带取消信号的兼容上下文。
func (c *Cache) GetMany(keys []string) (map[string]interface{}, error) {
	return c.GetManyContext(context.Background(), keys)
}

// GetManyContext 批量读取缓存，并在驱动支持时透传调用方上下文。
func (c *Cache) GetManyContext(ctx context.Context, keys []string) (map[string]interface{}, error) {
	if ctx == nil {
		return nil, ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(keys) > maxCacheBatchEntries {
		return nil, fmt.Errorf("%w: 最多 %d 个键", ErrCacheBatchTooLarge, maxCacheBatchEntries)
	}
	for _, key := range keys {
		if err := validatePublicCacheKey(key); err != nil {
			return nil, err
		}
	}
	driver, release, err := c.driver()
	if err != nil {
		return nil, err
	}
	defer release()
	for _, key := range keys {
		c.trace("GET", key)
	}
	values := make(map[string]interface{}, len(keys))
	if contextual, ok := driver.(ContextualBatchGetter); ok {
		values, err = contextual.GetManyContext(ctx, keys)
	} else if batch, ok := driver.(BatchGetter); ok {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, err = batch.GetMany(keys)
	} else {
		for _, key := range keys {
			var value interface{}
			var found bool
			value, found, err = getCacheValueContext(ctx, driver, key)
			if err != nil {
				break
			}
			if found {
				values[key] = value
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = make(map[string]interface{})
	}
	for key, raw := range values {
		value, visible, unwrapErr := c.visibleCacheValue(ctx, driver, key, raw)
		if unwrapErr != nil {
			return nil, unwrapErr
		}
		if visible {
			values[key] = value
		} else {
			delete(values, key)
		}
	}
	if err = c.repairBatchMissesContext(ctx, driver, keys, values); err != nil {
		return nil, err
	}
	return values, nil
}

// SetMany 批量写入缓存，省略 ttl 时使用 store 默认有效期。
func (c *Cache) SetMany(values map[string]interface{}, ttl ...time.Duration) error {
	return c.SetManyContext(context.Background(), values, ttl...)
}

// SetManyContext 批量写入缓存，并在驱动支持时透传调用方上下文。
func (c *Cache) SetManyContext(ctx context.Context, values map[string]interface{}, ttl ...time.Duration) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(values) > maxCacheBatchEntries {
		return fmt.Errorf("%w: 最多 %d 个键", ErrCacheBatchTooLarge, maxCacheBatchEntries)
	}
	resolvedTTL, err := c.resolveTTL(ttl)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if err := validatePublicCacheKey(key); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	for _, key := range keys {
		c.trace("SET", key)
	}
	writeValues := func() error {
		if cacheDriverSupportsAtomicFencing(driver) {
			return c.writeFencedCacheValuesContext(ctx, driver, keys, values, resolvedTTL)
		}
		var writeErr error
		if contextual, ok := driver.(ContextualBatchSetter); ok {
			writeErr = contextual.SetManyContext(ctx, values, resolvedTTL)
		} else if batch, ok := driver.(BatchSetter); ok {
			if err := ctx.Err(); err != nil {
				return err
			}
			writeErr = batch.SetMany(values, resolvedTTL)
		} else {
			for _, key := range keys {
				if writeErr = setCacheValueContext(ctx, driver, key, values[key], resolvedTTL); writeErr != nil {
					break
				}
			}
		}
		return writeErr
	}
	needsMetadataLock := false
	for _, key := range keys {
		tags, metadataErr := c.reverseTagsLockedContext(ctx, driver, key)
		if metadataErr != nil {
			return metadataErr
		}
		if len(tags) > 0 {
			needsMetadataLock = true
			break
		}
	}
	if !needsMetadataLock {
		return writeValues()
	}
	return c.withTagMutationLockContext(ctx, driver, func() error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		for _, key := range keys {
			if err := c.repairMissingKeyMetadataLockedContext(ctx, driver, key); err != nil {
				return err
			}
		}
		return writeValues()
	})
}
