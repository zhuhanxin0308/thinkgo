package cache

import (
	"context"
	"errors"
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
	if err := validateCacheNumber(value); err != nil {
		return err
	}
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
		if !fenced {
			committed, err := driver.(LockGuardedUpdater).UpdateIfLockOwnerContext(ctx, key, ttl, lock.key, lock.owner, func(interface{}, bool) (interface{}, bool, error) {
				return value, false, nil
			})
			if err != nil {
				return err
			}
			if !committed {
				return ErrCacheLockLost
			}
			return nil
		}
		floor, err := c.commitInvalidationForKey(ctx, driver, key)
		if err != nil {
			return err
		}
		for attempt := 0; attempt < maxCacheAtomicRebaseAttempts; attempt++ {
			generation, err := c.allocateFenceGenerationAboveContext(ctx, driver, floor)
			if err != nil {
				return err
			}
			var observed uint64
			committed, err := driver.(LockGuardedUpdater).UpdateIfLockOwnerContext(ctx, key, ttl, lock.key, lock.owner, func(raw interface{}, found bool) (interface{}, bool, error) {
				if found {
					_, current, enveloped, err := decodeCacheValueEnvelope(raw)
					if err != nil {
						return nil, false, err
					}
					if enveloped && current >= generation {
						observed = current
						return nil, false, errCacheSequenceBehindEnvelope
					}
				}
				return newCacheValueEnvelope(generation, value), false, nil
			})
			if errors.Is(err, errCacheSequenceBehindEnvelope) {
				currentSeq, seqErr := c.currentFenceSequenceContext(ctx, driver)
				if seqErr != nil {
					return seqErr
				}
				if observed > generation && currentSeq >= observed {
					return ErrCacheLockLost
				}
				floor = max(floor, observed)
				continue
			}
			if err != nil {
				return err
			}
			if !committed {
				return ErrCacheLockLost
			}
			return c.verifyFenceCommitContext(ctx, driver, key, generation)
		}
		return ErrCacheFenceUnavailable
	})
}
