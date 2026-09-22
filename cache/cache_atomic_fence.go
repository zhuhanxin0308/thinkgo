package cache

import (
	"context"
	"errors"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/framework/cache/contract"
)

var errCacheAtomicFenceStale = errors.New("缓存原子操作需要重新分配提交代际")

// maxCacheAtomicRebaseAttempts 与内置 Redis 单次乐观事务预算一致，限制持续竞争时的领票次数。
const maxCacheAtomicRebaseAttempts = 64

// withFreshAtomicFence 仅在后端尚未提交且票据过时时重入；真实失效与业务错误直接返回。
// 领取全局票据必须在单键原子锁之外，避免内存互斥锁或文件分片锁的嵌套死锁。
func (c *Cache) withFreshAtomicFence(ctx context.Context, driver Driver, key string, run func(uint64, uint64, uint64) error) (generation, origin uint64, err error) {
	if !cacheDriverSupportsAtomicFencing(driver) {
		return 0, 0, run(0, 0, 0)
	}
	for attempt := 0; attempt < maxCacheAtomicRebaseAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return generation, origin, err
		}
		generation, err = c.nextFenceGenerationContext(ctx, driver)
		if err != nil {
			return generation, origin, err
		}
		if origin == 0 {
			origin = generation
		}
		invalidation, readErr := c.invalidationForKey(ctx, driver, key)
		if readErr != nil {
			return generation, origin, readErr
		}
		if err = run(generation, origin, invalidation); !errors.Is(err, errCacheAtomicFenceStale) {
			return generation, origin, err
		}
	}
	return generation, origin, fmt.Errorf("%w: 重新分配提交代际超过 %d 次", contract.ErrAtomicUpdateConflict, maxCacheAtomicRebaseAttempts)
}

// decodeAtomicCacheValue 每次原子尝试都重新读取因果起点，不继承 WATCH 失败尝试的临时代际。
// 只有实际读取到仍有效的新值，旧操作才可作为其后续合并推进屏障；缺失或失效值不能复活。
func decodeAtomicCacheValue(raw interface{}, found bool, generation, started, invalidation uint64) (interface{}, bool, uint64, error) {
	origin := started
	var value interface{}
	if found {
		decoded, currentGeneration, currentOrigin, _, err := decodeCacheValueFence(raw)
		if err != nil {
			return nil, false, origin, err
		}
		if generation > 0 && currentGeneration >= generation {
			return nil, false, origin, errCacheAtomicFenceStale
		}
		if invalidation == 0 || currentOrigin > invalidation {
			value = decoded
			origin = max(origin, currentOrigin)
		} else {
			found = false
		}
	}
	if invalidation > origin {
		return nil, false, origin, ErrCacheLockLost
	}
	return value, found, origin, nil
}
