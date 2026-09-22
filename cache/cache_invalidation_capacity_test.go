package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// TestInvalidationCapacityFailsBeforeDeletingBusinessData 验证新范围达到上限即失败，已有范围仍可更新且全量失效可安全回收。
func TestInvalidationCapacityFailsBeforeDeletingBusinessData(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	keys := make([]string, 0, maxCacheInvalidationScopes)
	for index := 0; len(keys) < maxCacheInvalidationScopes; index++ {
		key := fmt.Sprintf("key:%d", index)
		if keyInvalidationBucket(key) == keyInvalidationBucket("untouched") {
			keys = append(keys, key)
		}
	}
	if _, err := manager.beginKeyInvalidation(context.Background(), backend, keys); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set("untouched", "keep"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Forget("untouched"); !errors.Is(err, ErrCacheInvalidationCapacity) {
		t.Fatalf("容量已满的新失效没有失败: %v", err)
	}
	if value, found, err := manager.Get("untouched"); err != nil || !found || value != "keep" {
		t.Fatalf("容量失败删除业务数据: %v %t %v", value, found, err)
	}
	if err := manager.Forget(keys[0]); err != nil {
		t.Fatalf("已有范围无法更新: %v", err)
	}
	if err := manager.ClearPrefix("new-prefix:"); err != nil {
		t.Fatalf("键桶满不应阻断独立前缀范围: %v", err)
	}
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Forget("untouched"); err != nil {
		t.Fatalf("全量失效没有回收容量: %v", err)
	}
}

// TestMultiBucketCapacityFailureKeepsEarlierKeys 验证多键失效不能在发现后续桶容量不足前先使其他键失效。
func TestMultiBucketCapacityFailureKeepsEarlierKeys(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	full := make([]string, 0, maxCacheInvalidationScopes)
	first, overflow := "", ""
	for index := 0; len(full) < maxCacheInvalidationScopes || first == "" || overflow == ""; index++ {
		key := fmt.Sprintf("capacity:%d", index)
		switch keyInvalidationBucket(key) {
		case 0:
			if first == "" {
				first = key
			}
		case 1:
			if len(full) < maxCacheInvalidationScopes {
				full = append(full, key)
			} else if overflow == "" {
				overflow = key
			}
		}
	}
	if _, err := manager.beginKeyInvalidation(context.Background(), backend, full); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(first, "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.beginKeyInvalidation(context.Background(), backend, []string{first, overflow}); !errors.Is(err, ErrCacheInvalidationCapacity) {
		t.Fatalf("后续桶容量不足没有报告: %v", err)
	}
	if value, found, err := manager.Get(first); err != nil || !found || value != "keep" {
		t.Fatalf("可预见的容量失败使前一个桶部分失效: %v %t %v", value, found, err)
	}
}

// TestPrefixCapacityIncludesEscapedBytes 验证最长合法前缀经 JSON 转义后也不会越过后端单条文件上限。
func TestPrefixCapacityIncludesEscapedBytes(t *testing.T) {
	backend, err := cacheDriver.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCache(nil, backend)
	watermarks := make(map[string]interface{})
	var rejected string
	for index := 0; index < maxCacheInvalidationScopes; index++ {
		prefix := fmt.Sprintf("%04d", index) + strings.Repeat("<", maxCacheKeyBytes-4)
		watermarks[prefixInvalidationScopePrefix+prefix] = "1"
		if _, _, err := boundedScopeWatermarks(watermarks); errors.Is(err, ErrCacheInvalidationCapacity) {
			delete(watermarks, prefixInvalidationScopePrefix+prefix)
			rejected = prefix
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if rejected == "" || len(watermarks) >= maxCacheInvalidationScopes {
		t.Fatal("前缀字节上限未先于数量上限发挥作用")
	}
	encoded, err := json.Marshal(watermarks)
	if err != nil || len(encoded) > maxCacheInvalidationBytes {
		t.Fatalf("安全前缀元数据编码超出预算: %d %v", len(encoded), err)
	}
	if err := backend.Set(manager.scopedInvalidationKey(), watermarks, 0); err != nil {
		t.Fatalf("合法边界元数据无法由真实文件后端存储: %v", err)
	}
	if err := manager.ClearPrefix(rejected); !errors.Is(err, ErrCacheInvalidationCapacity) {
		t.Fatalf("超字节预算未返回容量错误: %v", err)
	}
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := manager.ClearPrefix(rejected); err != nil {
		t.Fatalf("Flush 后字节预算未恢复: %v", err)
	}
}

// TestCompactionPreservesNewerScopes 验证较早的 Flush 不会回收更晚发布的精确键或前缀水位。
func TestCompactionPreservesNewerScopes(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	old, err := manager.beginKeyInvalidation(context.Background(), backend, []string{"old"})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := manager.beginKeyInvalidation(context.Background(), backend, []string{"new"})
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := manager.beginPrefixInvalidation(context.Background(), backend, "prefix:")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.compactScopesAtFence(context.Background(), backend, old); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]uint64{"old": 0, "new": newer, "prefix:value": prefix} {
		if actual, err := manager.invalidationForKey(context.Background(), backend, key); err != nil || actual != want {
			t.Fatalf("压缩丢失较新范围: %s actual=%d want=%d err=%v", key, actual, want, err)
		}
	}
}

type pausedGlobalReadDriver struct {
	*cacheDriver.Memory
	target  string
	started chan struct{}
	release chan struct{}
}

func (d *pausedGlobalReadDriver) Get(key string) (interface{}, bool, error) {
	value, found, err := d.Memory.Get(key)
	if key == d.target {
		close(d.started)
		<-d.release
	}
	return value, found, err
}

// TestFlushCompactionDoesNotHideReadFence 验证读取全局水位与压缩旧桶并发时不能放行失效的迟到信封。
func TestFlushCompactionDoesNotHideReadFence(t *testing.T) {
	backend := &pausedGlobalReadDriver{Memory: cacheDriver.NewMemory(), started: make(chan struct{}), release: make(chan struct{})}
	manager := NewCache(nil, backend)
	const key = "late"
	generation, err := manager.nextFenceGeneration(backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.beginKeyInvalidation(context.Background(), backend, []string{key}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Set(key, newCacheValueEnvelope(generation, "stale"), 0); err != nil {
		t.Fatal(err)
	}
	backend.target = manager.fenceInvalidationKey()
	done := make(chan error, 1)
	go func() {
		_, found, err := manager.Get(key)
		if found && err == nil {
			err = errors.New("回收范围后失效旧值变得可见")
		}
		done <- err
	}()
	<-backend.started
	flushErr := manager.Flush()
	close(backend.release)
	if err := <-done; err != nil || flushErr != nil {
		t.Fatalf("并发回收破坏读取水位: read=%v flush=%v", err, flushErr)
	}
}
