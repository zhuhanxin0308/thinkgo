package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

type legacyTTLAtomicDriver struct {
	Driver
	AtomicUpdater
	TTLAtomicUpdater
}

// TestCounterDoesNotInheritInvalidatedTTL 验证旧值的物理清理失败时，新计数仍从零创建且不继承其短 TTL。
func TestCounterDoesNotInheritInvalidatedTTL(t *testing.T) {
	for _, kind := range []string{"memory", "file", "redis"} {
		t.Run(kind, func(t *testing.T) {
			var backend Driver
			advance := time.Sleep
			switch kind {
			case "memory":
				backend = cacheDriver.NewMemory()
			case "file":
				file, err := cacheDriver.NewFile(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				backend = file
			case "redis":
				redis, clock := atomicOrderRedisWithClock(t)
				backend, advance = redis, clock.FastForward
			}
			assertCounterInvalidatedTTLAfter(t, backend, advance)
		})
	}
}

func assertCounterInvalidatedTTLAfter(t *testing.T, backend Driver, advance func(time.Duration)) {
	t.Helper()
	manager := NewCache(nil, backend)
	const ttl = time.Second
	for _, key := range []string{"origin-inc", "origin-dec"} {
		if err := manager.Set(key, int64(10), ttl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.beginKeyInvalidation(context.Background(), backend, []string{"origin-inc", "origin-dec"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"origin-inc", "origin-dec"} {
		if _, found, err := backend.Get(key); err != nil || !found {
			t.Fatalf("测试未保留真实的短 TTL 旧项: %t %v", found, err)
		}
	}
	if count, err := manager.Inc("origin-inc", 1); err != nil || count != 1 {
		t.Fatalf("新计数未从逻辑缺失值创建: %d %v", count, err)
	}
	if count, err := manager.Dec("origin-dec", 1); err != nil || count != -1 {
		t.Fatalf("新减量计数未从逻辑缺失值创建: %d %v", count, err)
	}
	advance(ttl + ttl/4)
	for key, expected := range map[string]int64{"origin-inc": 1, "origin-dec": -1} {
		if value, found, err := manager.Get(key); err != nil || !found || value != expected {
			t.Fatalf("新计数继承了失效旧项的 TTL: key=%s value=%v found=%t err=%v", key, value, found, err)
		}
	}
}

// TestLegacyCounterDriverRejectsTTLReset 明确旧扩展驱动的能力边界，禁止默默保留已经失效的 TTL。
func TestLegacyCounterDriverRejectsTTLReset(t *testing.T) {
	backend := cacheDriver.NewMemory()
	legacy := &legacyTTLAtomicDriver{Driver: backend, AtomicUpdater: backend, TTLAtomicUpdater: backend}
	manager := NewCache(nil, legacy)
	if err := manager.Set("counter", int64(10), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.beginKeyInvalidation(context.Background(), legacy, []string{"counter"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Inc("counter", 1); !errors.Is(err, ErrCacheAtomicUpdateUnsupported) {
		t.Fatalf("旧驱动缺少 TTL 重置能力却报告成功: %v", err)
	}
	if value, found, err := manager.Get("counter"); err != nil || found {
		t.Fatalf("失败关闭泄漏失效旧值: %v %t %v", value, found, err)
	}
	if count, err := manager.Inc("new-counter", 1); err != nil || count != 1 {
		t.Fatalf("旧驱动的真实缺失项创建不应受影响: %d %v", count, err)
	}
}
