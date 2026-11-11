package driver

import (
	"context"
	"time"
)

// UpdateWithExpiryContext 在内存写锁内读取并提交值及绝对过期时间，供失败批次安全恢复。
func (c *Memory) UpdateWithExpiryContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error)) error {
	if c == nil || ctx == nil || update == nil {
		return ErrNilAtomicUpdate
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	if c.budget != nil {
		c.snapshotLock.Lock()
		defer c.snapshotLock.Unlock()
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.updateAtomicExpiryLocked(key, ttl, func(value interface{}, found bool, expiry time.Time) (interface{}, bool, *time.Time, error) {
		next, remove, override, err := update(value, found, expiry)
		if err == nil {
			err = ctx.Err()
		}
		return next, remove, override, err
	})
}
