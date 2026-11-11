package driver

import (
	"context"
	"time"
)

// UpdateWithExpiryContext 在跨进程文件锁内读取值和原始过期时间，恢复时不重新计算 TTL。
func (c *File) UpdateWithExpiryContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error)) error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	if ctx == nil || update == nil {
		return ErrNilAtomicUpdate
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	return c.withMutationGuardContext(ctx, key, func() error {
		lock := c.keyLock(key)
		lock.Lock()
		defer lock.Unlock()
		return c.updateItemWithExpiryLocked(key, ttl, func(value interface{}, found bool, expiry time.Time) (interface{}, bool, *time.Time, error) {
			next, remove, override, err := update(value, found, expiry)
			if err == nil {
				err = ctx.Err()
			}
			return next, remove, override, err
		})
	})
}
