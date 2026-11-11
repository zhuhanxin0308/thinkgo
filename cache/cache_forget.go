package cache

import (
	"context"
	"errors"
)

// Forget 删除指定键并清理它的全部标签反向关系。
func (c *Cache) Forget(key string) error {
	return c.ForgetContext(context.Background(), key)
}

// ForgetContext 删除缓存键及标签关系，并让支持上下文的驱动响应请求取消。
func (c *Cache) ForgetContext(ctx context.Context, key string) error {
	if ctx == nil {
		return ErrInvalidCacheContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePublicCacheKey(key); err != nil {
		return err
	}
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	c.trace("FORGET", key)
	return c.withTagMutationLockContext(ctx, driver, func(ctx context.Context) error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		if cacheDriverSupportsAtomicFencing(driver) {
			fence, fenceErr := c.beginKeyInvalidation(ctx, driver, []string{key})
			if fenceErr != nil {
				return fenceErr
			}
			_, found, readErr := getCacheValueContext(ctx, driver, key)
			if readErr != nil {
				return readErr
			}
			if found {
				deleted, deleteErr := c.deleteAtFenceContext(ctx, driver, key, fence)
				if deleteErr != nil || !deleted {
					return deleteErr
				}
			}
			_, cleanupErr := c.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
			return cleanupErr
		}
		rollback, err := c.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
		if err != nil {
			return err
		}
		if err = deleteCacheValueContext(ctx, driver, key); err != nil {
			return errors.Join(err, rollback())
		}
		return nil
	})
}
