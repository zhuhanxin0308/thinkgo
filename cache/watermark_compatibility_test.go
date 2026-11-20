package cache

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 滚动升级中的旧读者只识别十进制水位；写者也不能向旧实现无法继续更新的桶追加条目。
func TestExactWatermarkRemainsReadableAfterWriterCrash(t *testing.T) {
	const legacyBucketCapacity = 256
	const key = "doc:retained"
	raw := driver.NewMemory()
	store := NewCache(nil, raw)
	issued, err := store.nextFenceGeneration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Forget(key); err != nil {
		t.Fatal(err)
	}
	bucket := keyInvalidationBucket(key)
	bucketKey := store.keyInvalidationBucketKey(bucket)
	metadata, found, err := raw.Get(bucketKey)
	if err != nil || !found {
		t.Fatalf("缺少精确水位：%v", err)
	}
	entry := metadata.(map[string]interface{})[keyInvalidationScope(key)]
	text, ok := entry.(string)
	if floor, err := strconv.ParseUint(text, 10, 64); !ok || err != nil || floor <= issued {
		t.Fatalf("旧读者不能解析已完成删除的水位：%#v", entry)
	}
	keys := make([]string, 0, legacyBucketCapacity)
	for i := 0; len(keys) < legacyBucketCapacity; i++ {
		peer := fmt.Sprintf("doc:peer-%d", i)
		if keyInvalidationBucket(peer) == bucket {
			keys = append(keys, peer)
		}
	}
	if _, err := store.beginKeyInvalidation(t.Context(), raw, keys[:len(keys)-1]); err != nil {
		t.Fatal(err)
	}
	if err := store.Forget(keys[len(keys)-1]); !errors.Is(err, ErrCacheInvalidationCapacity) {
		t.Fatalf("缺乏安全回收证据的满桶必须在改写前失败：%v", err)
	}
	// 模拟旧进程已越过写前检查，提交后崩溃，没有机会再做写后校验或回滚。
	if err := raw.Set(key, newCacheValueEnvelope(issued, "stale"), 0); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(key); err != nil || found {
		t.Fatalf("精确水位消失后旧写入复活：found=%t err=%v", found, err)
	}
	if values, err := store.GetMany([]string{key}); err != nil || len(values) != 0 {
		t.Fatalf("批量读放行了相同的崩溃写入：values=%v err=%v", values, err)
	}
}
