package cache

import (
	"context"
	"time"
)

func cacheDriverSupportsGuardedUpdates(driver Driver) bool {
	if capability, ok := driver.(interface{ supportsGuardedUpdates() bool }); ok && !capability.supportsGuardedUpdates() {
		return false
	}
	_, supported := driver.(LockGuardedUpdater)
	return supported
}

// commitRememberValue 在后端持有租约检查的原子区间内提交，并保持标签与失效代际语义。
func (c *Cache) commitRememberValue(ctx context.Context, key string, value interface{}, ttl time.Duration, lock *Lock) error {
	driver, release, err := c.driver()
	if err != nil {
		return err
	}
	defer release()
	if !cacheDriverSupportsGuardedUpdates(driver) {
		return ErrCacheLockUnsupported
	}
	c.trace("SET", key)
	return c.mutateWithTagRepairContext(ctx, driver, key, func() error {
		fenced := cacheDriverSupportsAtomicFencing(driver)
		generation := uint64(0)
		if fenced {
			var err error
			generation, err = c.nextFenceGenerationContext(ctx, driver)
			if err != nil {
				return err
			}
		}
		committed, err := driver.(LockGuardedUpdater).UpdateIfLockOwnerContext(ctx, key, ttl, lock.key, lock.owner, func(raw interface{}, found bool) (interface{}, bool, error) {
			if !fenced {
				return value, false, nil
			}
			if found {
				_, current, enveloped, err := decodeCacheValueEnvelope(raw)
				if err != nil {
					return nil, false, err
				}
				if enveloped && current > generation {
					return nil, false, ErrCacheLockLost
				}
			}
			return newCacheValueEnvelope(generation, value), false, nil
		})
		if err != nil {
			return err
		}
		if !committed {
			return ErrCacheLockLost
		}
		if fenced {
			return c.verifyFenceCommitContext(ctx, driver, key, generation)
		}
		return nil
	})
}
