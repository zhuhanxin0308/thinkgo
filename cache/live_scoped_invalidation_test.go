//go:build integration

package cache

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	redisDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver/redis"
)

func liveScopedRedis(tb testing.TB) (*Cache, *Cache) {
	tb.Helper()
	host := os.Getenv("THINKGO_LIVE_REDIS_HOST")
	portText := os.Getenv("THINKGO_LIVE_REDIS_PORT")
	if host == "" || portText == "" {
		tb.Skip("THINKGO_LIVE_REDIS_* 未配置")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		tb.Fatal(err)
	}
	config := map[string]interface{}{
		"host": host, "port": port,
		"prefix": fmt.Sprintf("thinkgo:scope:%d:%d:", os.Getpid(), time.Now().UnixNano()),
	}
	if value := os.Getenv("THINKGO_LIVE_REDIS_DB"); value != "" {
		index, err := strconv.Atoi(value)
		if err != nil {
			tb.Fatal(err)
		}
		config["select"] = index
	}
	if value, found := os.LookupEnv("THINKGO_LIVE_REDIS_PASSWORD"); found {
		config["password"] = value
	}
	backend, err := redisDriver.NewRedis(config)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = backend.Close() })
	managers := make([]*Cache, 0, 2)
	for _, prefix := range []string{"tenant:a:", "tenant:b:"} {
		namespace, err := NewNamespaceDriver(backend, prefix, "tag:")
		if err != nil {
			tb.Fatal(err)
		}
		manager := NewCache(nil, namespace)
		managers = append(managers, manager)
		tb.Cleanup(func() {
			keys := []string{manager.fenceSequenceKey(), manager.fenceInvalidationKey(), manager.scopedInvalidationKey()}
			for bucket := 0; bucket < cacheInvalidationBuckets; bucket++ {
				keys = append(keys, manager.keyInvalidationBucketKey(bucket))
			}
			for _, key := range keys {
				if err := namespace.Delete(key); err != nil {
					tb.Errorf("清理唯一测试 namespace 代际失败: %v", err)
				}
			}
			if err := namespace.Clear(); err != nil {
				tb.Errorf("清理唯一测试 namespace 数据失败: %v", err)
			}
		})
	}
	return managers[0], managers[1]
}

// TestLiveRedisScopedInvalidation 验证实际 Redis namespace、标签失效、旧写者读取边界以及 Flush 保留活动锁。
func TestLiveRedisScopedInvalidation(t *testing.T) {
	first, second := liveScopedRedis(t)
	for _, manager := range []*Cache{first, second} {
		if err := manager.Set("session:active", "saved", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	tagged, err := first.Tag("products")
	if err != nil {
		t.Fatal(err)
	}
	if err := tagged.Set("product:1", "old", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := tagged.Flush(); err != nil {
		t.Fatal(err)
	}
	if value, found, err := first.Get("session:active"); err != nil || !found || value != "saved" {
		t.Fatalf("标签失效误伤 Session: %v %t %v", value, found, err)
	}
	backend, release, err := first.driver()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old, err := first.nextFenceGeneration(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ClearPrefix("session:"); err != nil {
		t.Fatal(err)
	}
	// 模拟旧进程在清理完成后才提交到 Redis，然后停在应用层后置校验之前。
	if err := backend.Set("session:active", newCacheValueEnvelope(old, "stale"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, found, err := first.Get("session:active"); err != nil || found {
		t.Fatalf("真实 Redis 暴露失效旧值: %t %v", found, err)
	}
	if value, found, err := second.Get("session:active"); err != nil || !found || value != "saved" {
		t.Fatalf("前缀清理越过 tenant 边界: %v %t %v", value, found, err)
	}
	lock, err := first.Lock("held", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if acquired, err := lock.AcquireContext(context.Background()); err != nil || !acquired {
		t.Fatalf("准备 namespace 活动锁失败: %t %v", acquired, err)
	}
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}
	if released, err := lock.ReleaseContext(context.Background()); err != nil || !released {
		t.Fatalf("Flush 删除 namespace 活动锁: %t %v", released, err)
	}
}

// BenchmarkLiveRedisScopedReads 测量真实 Redis 中高基数精确失效范围下的业务读路径。
func BenchmarkLiveRedisScopedReads(b *testing.B) {
	manager, _ := liveScopedRedis(b)
	benchmarkScopedReads(b, manager, 1000)
}
