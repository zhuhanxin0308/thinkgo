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
	if contextual, ok := driver.(ContextualBatchGetter); ok {
		values, err := contextual.GetManyContext(ctx, keys)
		if err != nil {
			return nil, err
		}
		if values == nil {
			return map[string]interface{}{}, nil
		}
		return values, nil
	}
	if batch, ok := driver.(BatchGetter); ok {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, err := batch.GetMany(keys)
		if err != nil {
			return nil, err
		}
		if values == nil {
			return map[string]interface{}{}, nil
		}
		return values, nil
	}
	values := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		value, found, err := getCacheValueContext(ctx, driver, key)
		if err != nil {
			return nil, err
		}
		if found {
			values[key] = value
		}
	}
	return values, nil
}

// SetMany 批量写入缓存，默认使用不带取消信号的兼容上下文。
func (c *Cache) SetMany(values map[string]interface{}, ttl time.Duration) error {
	return c.SetManyContext(context.Background(), values, ttl)
}

// SetManyContext 批量写入缓存，并在驱动支持时透传调用方上下文。
func (c *Cache) SetManyContext(ctx context.Context, values map[string]interface{}, ttl time.Duration) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(values) > maxCacheBatchEntries {
		return fmt.Errorf("%w: 最多 %d 个键", ErrCacheBatchTooLarge, maxCacheBatchEntries)
	}
	if err := validateCacheTTL(ttl); err != nil {
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
	c.state.tagMu.RLock()
	defer c.state.tagMu.RUnlock()
	if contextual, ok := driver.(ContextualBatchSetter); ok {
		return contextual.SetManyContext(ctx, values, ttl)
	}
	if batch, ok := driver.(BatchSetter); ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		return batch.SetMany(values, ttl)
	}
	for _, key := range keys {
		if err := setCacheValueContext(ctx, driver, key, values[key], ttl); err != nil {
			return err
		}
	}
	return nil
}
