package cache

import (
	"context"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/contract"
)

// getCacheValueAndRepairContext 在未命中时检查反向元数据，并在代际锁内二次读取后修复幽灵关系。
func (c *Cache) getCacheValueAndRepairContext(ctx context.Context, driver Driver, key string) (interface{}, bool, error) {
	value, found, err := c.getVisibleCacheValue(ctx, driver, key)
	if err != nil {
		return nil, false, err
	}
	if found {
		return value, true, nil
	}
	tags, err := c.reverseTagsLockedContext(ctx, driver, key)
	if err != nil || len(tags) == 0 {
		return nil, false, err
	}
	err = c.withTagMutationLockContext(ctx, driver, func() error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		value, found, err = c.getVisibleCacheValue(ctx, driver, key)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		_, err = c.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
		return err
	})
	return value, found, err
}

// hasCacheValueAndRepairContext 保留驱动的 Has 快速路径，只有未命中且存在反向关系时才进入修复锁。
func (c *Cache) hasCacheValueAndRepairContext(ctx context.Context, driver Driver, key string) (bool, error) {
	var (
		found bool
		err   error
	)
	if contextual, ok := driver.(ContextualHasser); ok {
		found, err = contextual.HasContext(ctx, key)
	} else {
		found, err = driver.Has(key)
	}
	if err != nil || found && !cacheDriverSupportsAtomicFencing(driver) {
		return found, err
	}
	_, found, err = c.getCacheValueAndRepairContext(ctx, driver, key)
	return found, err
}

// repairBatchMissesContext 将批量未命中的幽灵关系合并到一次代际锁内清理。
func (c *Cache) repairBatchMissesContext(ctx context.Context, driver Driver, keys []string, values map[string]interface{}) error {
	candidates := make([]string, 0)
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, found := values[key]; found {
			continue
		}
		tags, err := c.reverseTagsLockedContext(ctx, driver, key)
		if err != nil {
			return err
		}
		if len(tags) > 0 {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return c.withTagMutationLockContext(ctx, driver, func() error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		for _, key := range candidates {
			value, found, err := c.getVisibleCacheValue(ctx, driver, key)
			if err != nil {
				return err
			}
			if found {
				values[key] = value
				continue
			}
			if _, err = c.unbindKeyFromAllTagsLockedContext(ctx, driver, key); err != nil {
				return err
			}
		}
		return nil
	})
}

// repairMissingKeyMetadataLockedContext 只在业务值确实缺失时清理旧标签关系。
// 调用方必须已经持有标签代际锁和进程内 tagMu 写锁。
func (c *Cache) repairMissingKeyMetadataLockedContext(ctx context.Context, driver Driver, key string) error {
	_, found, err := c.getVisibleCacheValue(ctx, driver, key)
	if err != nil || found {
		return err
	}
	_, err = c.unbindKeyFromAllTagsLockedContext(ctx, driver, key)
	return err
}

// mutateWithTagRepairContext 只让已有标签关系的键进入标签锁；
// 普通无标签键依靠原子代际提交，避免所有 Session 和业务缓存被一个全局锁串行化。
func (c *Cache) mutateWithTagRepairContext(ctx context.Context, driver Driver, key string, mutation func() error) error {
	tags, err := c.reverseTagsLockedContext(ctx, driver, key)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return mutation()
	}
	return c.withTagMutationLockContext(ctx, driver, func() error {
		c.state.tagMu.Lock()
		defer c.state.tagMu.Unlock()
		if err := c.repairMissingKeyMetadataLockedContext(ctx, driver, key); err != nil {
			return err
		}
		return mutation()
	})
}

// setCacheValueWithFenceContext 让无标签写入直接走单键代际，有标签键再进入元数据锁。
func (c *Cache) setCacheValueWithFenceContext(ctx context.Context, driver Driver, key string, value interface{}, ttl time.Duration) error {
	return c.mutateWithTagRepairContext(ctx, driver, key, func() error {
		return c.writeFencedCacheValueContext(ctx, driver, key, value, ttl)
	})
}

// changeCounterWithFence 在保留现有 TTL 的单键原子边界内更新计数，并拒绝跨越更新后的失效代际。
func (c *Cache) changeCounterWithFence(driver Driver, key string, step int64, subtract bool) (value int64, resultErr error) {
	resultErr = c.mutateWithTagRepairContext(context.Background(), driver, key, func() error {
		if !cacheDriverSupportsAtomicFencing(driver) {
			if subtract {
				value, resultErr = driver.Dec(key, step)
			} else {
				value, resultErr = driver.Inc(key, step)
			}
			return resultErr
		}
		committedOrigin := uint64(0)
		generation, _, err := c.withFreshAtomicFence(context.Background(), driver, key, func(generation, started, invalidation uint64) error {
			return atomicUpdateCacheValueConditionalTTL(driver, key, func(raw interface{}, found bool) (interface{}, bool, bool, error) {
				decoded, found, origin, decodeErr := decodeAtomicCacheValue(raw, found, generation, started, invalidation)
				if decodeErr != nil {
					return nil, false, false, decodeErr
				}
				current := int64(0)
				if found {
					current, decodeErr = strictCacheCounterValue(decoded)
					if decodeErr != nil {
						return nil, false, false, decodeErr
					}
				}
				var calculateErr error
				if subtract {
					value, calculateErr = subtractCacheCounter(current, step)
				} else {
					value, calculateErr = addCacheCounter(current, step)
				}
				if calculateErr != nil {
					return nil, false, false, calculateErr
				}
				committedOrigin = origin
				return newAtomicCacheValueEnvelope(generation, origin, value), false, found, nil
			})
		})
		if err != nil {
			return err
		}
		return c.verifyAtomicFenceCommitContext(context.Background(), driver, key, generation, committedOrigin)
	})
	return value, resultErr
}

func strictCacheCounterValue(value interface{}) (int64, error) {
	return contract.StrictCounterValue(value)
}

func addCacheCounter(value, step int64) (int64, error) {
	return contract.CheckedCounterAdd(value, step)
}

func subtractCacheCounter(value, step int64) (int64, error) {
	return contract.CheckedCounterSubtract(value, step)
}
