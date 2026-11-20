package cache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

type fenceHistoryAllocator interface {
	AllocateFenceGenerationContext(context.Context, string, string, uint64, uint64) (uint64, error)
	FenceGenerationFloorContext(context.Context, string, string) (uint64, error)
}

// redisClearNamespaceFilter 在两次扫描都识别子空间，避免预检后首次出现的子空间被误删。
func redisClearNamespaceFilter(seen *atomic.Bool) func(string) bool {
	return func(key string) bool {
		if _, namespaced, valid := cachekey.UnframeNamespaces(key); namespaced && valid {
			seen.Store(true)
			return false
		}
		return true
	}
}

func (c *Cache) fenceAllocator(driver Driver) (fenceHistoryAllocator, string, string) {
	sequence := c.fenceSequenceKey()
	issued := c.fenceIssuedKey()
	// 两份票号记录必须经由完全相同的命名空间包装层映射。
	for {
		ns, ok := driver.(*NamespaceDriver)
		if !ok {
			break
		}
		sequence, issued = ns.key(sequence), ns.key(issued)
		driver = ns.driver
	}
	allocator, _ := driver.(fenceHistoryAllocator)
	return allocator, sequence, issued
}

func (c *Cache) fenceIssuedKey() string {
	return cacheFencePrefix + cacheMetadataScope + ":issued"
}

// clearGenerationFloor 在发布 Redis 清理水位前核验已有信封，避免把票号缺失误当成空缓存。
func (c *Cache) clearGenerationFloor(ctx context.Context, driver Driver, clearer ConditionalPrefixClearer, prefix string) (uint64, error) {
	allocator, sequence, issued := c.fenceAllocator(driver)
	if allocator == nil {
		return 0, nil
	}
	var mu sync.Mutex
	var observed uint64
	var descendant atomic.Bool
	err := clearer.ClearPrefixIfContext(ctx, prefix, redisClearNamespaceFilter(&descendant), func(raw any) (bool, error) {
		_, generation, _, err := decodeCacheValueEnvelope(raw)
		if err != nil {
			return false, err
		}
		mu.Lock()
		observed = max(observed, generation)
		mu.Unlock()
		return false, nil
	})
	if err != nil {
		return 0, err
	}
	if descendant.Load() {
		// 子空间使用独立票号，父空间的水位不能识别其并发新写入。
		return 0, fmt.Errorf("%w: 清理范围包含独立发号的子命名空间", ErrCacheFenceUnavailable)
	}
	floor, err := allocator.FenceGenerationFloorContext(ctx, sequence, issued)
	if err != nil {
		return 0, err
	}
	if observed > floor {
		// 已知物理值仅提供下界，不能据此推断在途旧写者的票号上界。
		return 0, fmt.Errorf("%w: 已存信封代际 %d 超过已知票号 %d", ErrCacheFenceUnavailable, observed, floor)
	}
	if floor == 0 {
		keys := []string{c.fenceInvalidationKey(), c.scopedInvalidationKey()}
		for bucket := range cacheInvalidationBuckets {
			keys = append(keys, c.keyInvalidationBucketKey(bucket))
		}
		// Redis 与命名空间包装均支持保留逐键错误的批量读取。
		batch, ok := driver.(strictBatchGetter)
		if !ok {
			return 0, ErrCacheFenceUnavailable
		}
		metadata, readErr := batch.GetManyStrictContext(ctx, keys)
		if readErr != nil {
			return 0, readErr
		}
		if len(metadata) > 0 {
			return 0, fmt.Errorf("%w: 失效元数据仍在但两份发号记录均已丢失", ErrCacheFenceUnavailable)
		}
	}
	return floor, nil
}
