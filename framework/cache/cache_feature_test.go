package cache

import (
	"testing"
	"time"

	cacheDriver "thinkgo/framework/cache/driver"
)

func TestCacheStoresTagsAndLocks(t *testing.T) {
	cache := NewCache(nil, cacheDriver.NewMemory())
	cache.RegisterStore("secondary", cacheDriver.NewMemory())

	cache.Set("counter", int64(1), 0)
	if value := cache.Inc("counter", 2); value != 3 {
		t.Fatalf("Inc 结果不正确，实际为 %d", value)
	}
	if value := cache.Dec("counter", 1); value != 2 {
		t.Fatalf("Dec 结果不正确，实际为 %d", value)
	}

	cache.Store("secondary").Set("profile", "secondary-store", 0)
	if value := cache.Get("profile"); value != nil {
		t.Fatalf("默认 store 不应读到 secondary 的数据，实际为 %#v", value)
	}
	if value := cache.Store("secondary").Get("profile"); value != "secondary-store" {
		t.Fatalf("secondary store 读取失败，实际为 %#v", value)
	}

	tagged := cache.Tag("users", "profile")
	tagged.Set("user:1", "张三", time.Minute)
	if value := cache.Get("user:1"); value != "张三" {
		t.Fatalf("Tag 写入应落到缓存中，实际为 %#v", value)
	}
	cache.Tag("users").Flush()
	if value := cache.Get("user:1"); value != nil {
		t.Fatalf("Tag Flush 后应清理关联缓存，实际为 %#v", value)
	}

	lockA := cache.Lock("sync-job", time.Second)
	if !lockA.Acquire() {
		t.Fatal("第一个锁应获取成功")
	}
	lockB := cache.Lock("sync-job", time.Second)
	if lockB.Acquire() {
		t.Fatal("未释放前第二个锁不应获取成功")
	}
	if !lockA.Release() {
		t.Fatal("锁释放应成功")
	}
	if !lockB.Acquire() {
		t.Fatal("释放后第二个锁应能获取成功")
	}
}

// TestCacheWithoutDriverIsNoop 验证未配置缓存驱动时公开 API 不会 panic。
func TestCacheWithoutDriverIsNoop(t *testing.T) {
	cache := NewCache(nil, nil)

	cache.Set("missing", "value", time.Minute)
	if value := cache.Get("missing"); value != nil {
		t.Fatalf("无驱动缓存读取应返回 nil，实际为 %#v", value)
	}
	if cache.Has("missing") {
		t.Fatal("无驱动缓存不应报告键存在")
	}
	cache.Forever("forever", "value")
	cache.Forget("missing")
	cache.Flush()

	if value := cache.Remember("remember", time.Minute, func() interface{} {
		return "computed"
	}); value != "computed" {
		t.Fatalf("无驱动 Remember 应返回回调结果，实际为 %#v", value)
	}
	if value := cache.Inc("counter", 3); value != 0 {
		t.Fatalf("无驱动 Inc 应返回 0，实际为 %d", value)
	}
	if value := cache.Dec("counter", 2); value != 0 {
		t.Fatalf("无驱动 Dec 应返回 0，实际为 %d", value)
	}
}
