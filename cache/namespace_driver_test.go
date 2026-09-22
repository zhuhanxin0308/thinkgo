package cache

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// TestNamespaceDriverAppliesThinkPHPPrefixes 验证普通键、标签元数据键、
// 计数器和批量操作都只在底层驱动看到物理命名空间。
func TestNamespaceDriverAppliesThinkPHPPrefixes(t *testing.T) {
	backend := cacheDriver.NewMemory()
	driver, err := NewNamespaceDriver(backend, "tenant:", "cache_tag:")
	if err != nil {
		t.Fatalf("创建命名空间驱动失败: %v", err)
	}
	if driver.key("order") != "tenant:order" ||
		driver.key(tagMetaPrefix+"users") != "tenant:cache_tag:users" ||
		driver.key(tagReversePrefix+"order") != "tenant:cache_tag:reverse:order" {
		t.Fatalf("命名空间键映射错误: normal=%q tag=%q reverse=%q", driver.key("order"), driver.key(tagMetaPrefix+"users"), driver.key(tagReversePrefix+"order"))
	}

	if err = driver.Set("order", "paid", time.Minute); err != nil {
		t.Fatalf("命名空间 Set 失败: %v", err)
	}
	if value, found, getErr := driver.Get("order"); getErr != nil || !found || value != "paid" {
		t.Fatalf("命名空间 Get 错误: value=%#v found=%t err=%v", value, found, getErr)
	}
	if value, found, getErr := backend.Get("tenant:order"); getErr != nil || !found || value != "paid" {
		t.Fatalf("底层物理键错误: value=%#v found=%t err=%v", value, found, getErr)
	}
	if found, hasErr := driver.Has("order"); hasErr != nil || !found {
		t.Fatalf("命名空间 Has 错误: found=%t err=%v", found, hasErr)
	}
	if err = driver.Set("counter", int64(10), 0); err != nil {
		t.Fatalf("预置计数器失败: %v", err)
	}
	if value, incrementErr := driver.Inc("counter", 3); incrementErr != nil || value != 13 {
		t.Fatalf("命名空间 Inc 错误: value=%d err=%v", value, incrementErr)
	}
	if value, decrementErr := driver.Dec("counter", 5); decrementErr != nil || value != 8 {
		t.Fatalf("命名空间 Dec 错误: value=%d err=%v", value, decrementErr)
	}

	if err = driver.SetMany(map[string]interface{}{"one": 1, "two": 2}, time.Minute); err != nil {
		t.Fatalf("命名空间批量写入失败: %v", err)
	}
	values, err := driver.GetMany([]string{"one", "two", "missing"})
	if err != nil || !reflect.DeepEqual(values, map[string]interface{}{"one": 1, "two": 2}) {
		t.Fatalf("命名空间批量读取错误: values=%#v err=%v", values, err)
	}
	if err = driver.Delete("order"); err != nil {
		t.Fatalf("命名空间 Delete 失败: %v", err)
	}
	if found, _ := backend.Has("tenant:order"); found {
		t.Fatal("Delete 必须删除物理命名空间键")
	}
	if err = driver.Clear(); err != nil {
		t.Fatalf("命名空间 Clear 失败: %v", err)
	}
	if found, _ := driver.Has("one"); found {
		t.Fatal("Clear 必须清空当前命名空间")
	}
}

// TestNamespaceDriverClearPreservesSharedBackendNamespaces 验证同一后端上的前缀清理
// 不会删除其它租户的数据，并且同步与上下文入口保持相同边界。
func TestNamespaceDriverClearPreservesSharedBackendNamespaces(t *testing.T) {
	backend := cacheDriver.NewMemory()
	tenantA, err := NewNamespaceDriver(backend, "tenant-a:", "tag:")
	if err != nil {
		t.Fatalf("创建租户 A 驱动失败: %v", err)
	}
	tenantB, err := NewNamespaceDriver(backend, "tenant-b:", "tag:")
	if err != nil {
		t.Fatalf("创建租户 B 驱动失败: %v", err)
	}
	if err = tenantA.Set("profile", "A", time.Minute); err != nil {
		t.Fatalf("写入租户 A 失败: %v", err)
	}
	if err = tenantB.Set("profile", "B", time.Minute); err != nil {
		t.Fatalf("写入租户 B 失败: %v", err)
	}
	if err = tenantA.Clear(); err != nil {
		t.Fatalf("清理租户 A 失败: %v", err)
	}
	if _, found, getErr := tenantA.Get("profile"); getErr != nil || found {
		t.Fatalf("租户 A 数据仍然存在: found=%t err=%v", found, getErr)
	}
	if value, found, getErr := tenantB.Get("profile"); getErr != nil || !found || value != "B" {
		t.Fatalf("租户 A 清理误删租户 B: value=%#v found=%t err=%v", value, found, getErr)
	}

	if err = tenantA.Set("profile", "A2", time.Minute); err != nil {
		t.Fatalf("重新写入租户 A 失败: %v", err)
	}
	if err = tenantA.ClearContext(context.Background()); err != nil {
		t.Fatalf("上下文清理租户 A 失败: %v", err)
	}
	if value, found, getErr := tenantB.Get("profile"); getErr != nil || !found || value != "B" {
		t.Fatalf("上下文清理误删租户 B: value=%#v found=%t err=%v", value, found, getErr)
	}
}

// TestNamespaceDriverKeepsFenceOutsideBusinessPrefix 验证命名空间 Clear 只删除业务区，
// 单调 fencing 元数据使用独立摘要空间并在清理后继续递增。
func TestNamespaceDriverKeepsFenceOutsideBusinessPrefix(t *testing.T) {
	backend := cacheDriver.NewMemory()
	namespaced, err := NewNamespaceDriver(backend, "tenant:", "tag:")
	if err != nil {
		t.Fatalf("创建命名空间驱动失败: %v", err)
	}
	manager := NewCache(nil, namespaced)
	if err = manager.Set("profile", "Ada", time.Minute); err != nil {
		t.Fatalf("写入命名空间缓存失败: %v", err)
	}
	physicalFence := namespaced.key(manager.fenceSequenceKey())
	if strings.HasPrefix(physicalFence, "tenant:") {
		t.Fatalf("fencing 元数据落入业务清理前缀: %q", physicalFence)
	}
	if _, found, getErr := backend.Get(physicalFence); getErr != nil || !found {
		t.Fatalf("未写入命名空间 fencing 元数据: found=%t err=%v", found, getErr)
	}
	if err = manager.Flush(); err != nil {
		t.Fatalf("清空命名空间失败: %v", err)
	}
	if _, found, getErr := backend.Get(physicalFence); getErr != nil || !found {
		t.Fatalf("命名空间清理重置了 fencing 元数据: found=%t err=%v", found, getErr)
	}
	if err = manager.Set("profile", "Grace", time.Minute); err != nil {
		t.Fatalf("清理后的新一代写入失败: %v", err)
	}
	if value, found, getErr := manager.Get("profile"); getErr != nil || !found || value != "Grace" {
		t.Fatalf("清理后的命名空间值错误: value=%#v found=%t err=%v", value, found, getErr)
	}
}

// TestNamespaceDriverClearFailsClosedWithoutPrefixCapability 验证旧驱动缺少局部清理能力时
// 不会回退到危险的全后端 Clear。
func TestNamespaceDriverClearFailsClosedWithoutPrefixCapability(t *testing.T) {
	backend := &clearTrackingLegacyDriver{thinkPHPStoreDriver: thinkPHPStoreDriver{}}
	driver, err := NewNamespaceDriver(backend, "tenant:", "tag:")
	if err != nil {
		t.Fatalf("创建旧命名空间驱动失败: %v", err)
	}
	if err = driver.Clear(); !errors.Is(err, ErrCacheNamespaceClearUnsupported) {
		t.Fatalf("缺少前缀能力必须失败关闭: %v", err)
	}
	if backend.cleared {
		t.Fatal("命名空间清理不得回退到底层全量 Clear")
	}
}

type clearTrackingLegacyDriver struct {
	thinkPHPStoreDriver
	cleared bool
}

func (driver *clearTrackingLegacyDriver) Clear() error {
	driver.cleared = true
	return nil
}

// TestNamespaceDriverPropagatesContextBatchAndLocks 验证支持上下文的后端会
// 收到原始 context，并保持批量键还原与锁键隔离。
func TestNamespaceDriverPropagatesContextBatchAndLocks(t *testing.T) {
	backend := &contextTrackingCacheDriver{Memory: cacheDriver.NewMemory()}
	driver, err := NewNamespaceDriver(backend, "tenant:", "")
	if err != nil {
		t.Fatalf("创建上下文命名空间驱动失败: %v", err)
	}
	ctx := context.WithValue(context.Background(), cacheContextKey{}, "namespace")

	if err = driver.SetContext(ctx, "profile", "Ada", time.Minute); err != nil {
		t.Fatalf("SetContext 失败: %v", err)
	}
	if value, found, getErr := driver.GetContext(ctx, "profile"); getErr != nil || !found || value != "Ada" {
		t.Fatalf("GetContext 错误: value=%#v found=%t err=%v", value, found, getErr)
	}
	if found, hasErr := driver.HasContext(ctx, "profile"); hasErr != nil || !found {
		t.Fatalf("HasContext 错误: found=%t err=%v", found, hasErr)
	}
	if err = driver.SetManyContext(ctx, map[string]interface{}{"first": 1, "second": 2}, time.Minute); err != nil {
		t.Fatalf("SetManyContext 失败: %v", err)
	}
	values, err := driver.GetManyContext(ctx, []string{"first", "second"})
	if err != nil || !reflect.DeepEqual(values, map[string]interface{}{"first": 1, "second": 2}) {
		t.Fatalf("GetManyContext 错误: values=%#v err=%v", values, err)
	}
	if acquired, lockErr := driver.AcquireLockContext(ctx, "checkout", "owner", time.Minute); lockErr != nil || !acquired {
		t.Fatalf("AcquireLockContext 错误: acquired=%t err=%v", acquired, lockErr)
	}
	if renewed, renewErr := driver.RenewLockContext(ctx, "checkout", "owner", time.Minute); renewErr != nil || !renewed {
		t.Fatalf("RenewLockContext 错误: renewed=%t err=%v", renewed, renewErr)
	}
	if released, releaseErr := driver.ReleaseLockContext(ctx, "checkout", "owner"); releaseErr != nil || !released {
		t.Fatalf("ReleaseLockContext 错误: released=%t err=%v", released, releaseErr)
	}
	if err = driver.DeleteContext(ctx, "profile"); err != nil {
		t.Fatalf("DeleteContext 失败: %v", err)
	}
	if err = driver.ClearContext(ctx); err != nil {
		t.Fatalf("ClearContext 失败: %v", err)
	}

	getContext, setContext, getManyContext, setManyContext, hasContext, deleteContext, clearContext, lockContext := backend.contexts()
	for name, received := range map[string]context.Context{
		"GetContext":     getContext,
		"SetContext":     setContext,
		"GetManyContext": getManyContext,
		"SetManyContext": setManyContext,
		"HasContext":     hasContext,
		"DeleteContext":  deleteContext,
		"ClearContext":   clearContext,
		"LockContext":    lockContext,
	} {
		if received == nil || received.Value(cacheContextKey{}) != "namespace" {
			t.Errorf("%s 未收到原始 context", name)
		}
	}
}

// TestNamespaceDriverFallbackCapabilitiesAndIdentity 验证传统驱动的逐键、
// 锁与关闭回退，以及资源身份会透传给 store 隔离检查。
func TestNamespaceDriverFallbackCapabilitiesAndIdentity(t *testing.T) {
	legacyBackend := cacheDriver.NewMemory()
	driver, err := NewNamespaceDriver(legacyBackend, "legacy:", "")
	if err != nil {
		t.Fatalf("创建传统命名空间驱动失败: %v", err)
	}
	ctx := context.Background()
	if err = driver.SetContext(ctx, "one", 1, time.Minute); err != nil {
		t.Fatalf("传统 SetContext 回退失败: %v", err)
	}
	if value, found, getErr := driver.GetContext(ctx, "one"); getErr != nil || !found || value != 1 {
		t.Fatalf("传统 GetContext 回退错误: value=%#v found=%t err=%v", value, found, getErr)
	}
	if found, hasErr := driver.HasContext(ctx, "one"); hasErr != nil || !found {
		t.Fatalf("传统 HasContext 回退错误: found=%t err=%v", found, hasErr)
	}
	if err = driver.SetManyContext(ctx, map[string]interface{}{"two": 2, "three": 3}, time.Minute); err != nil {
		t.Fatalf("传统 SetManyContext 回退失败: %v", err)
	}
	if values, getErr := driver.GetManyContext(ctx, []string{"two", "three"}); getErr != nil || len(values) != 2 {
		t.Fatalf("传统 GetManyContext 回退错误: values=%#v err=%v", values, getErr)
	}
	if acquired, lockErr := driver.AcquireLock("lock", "owner", time.Minute); lockErr != nil || !acquired {
		t.Fatalf("传统 AcquireLock 错误: acquired=%t err=%v", acquired, lockErr)
	}
	if renewed, renewErr := driver.RenewLock("lock", "owner", time.Minute); renewErr != nil || !renewed {
		t.Fatalf("传统 RenewLock 错误: renewed=%t err=%v", renewed, renewErr)
	}
	if released, releaseErr := driver.ReleaseLock("lock", "owner"); releaseErr != nil || !released {
		t.Fatalf("传统 ReleaseLock 错误: released=%t err=%v", released, releaseErr)
	}
	if err = driver.DeleteContext(ctx, "one"); err != nil {
		t.Fatalf("传统 DeleteContext 回退失败: %v", err)
	}
	if err = driver.ClearContext(ctx); err != nil {
		t.Fatalf("传统 ClearContext 回退失败: %v", err)
	}
	if err = driver.Close(); err != nil {
		t.Fatalf("无 Close 能力的传统驱动应幂等成功: %v", err)
	}

	unsupported, err := NewNamespaceDriver(&thinkPHPStoreDriver{}, "plain:", "")
	if err != nil {
		t.Fatalf("创建无锁命名空间驱动失败: %v", err)
	}
	if _, err = unsupported.AcquireLock("lock", "owner", time.Minute); !errors.Is(err, ErrCacheLockUnsupported) {
		t.Fatalf("无锁驱动 AcquireLock 必须返回 ErrCacheLockUnsupported: %v", err)
	}
	if _, err = unsupported.ReleaseLock("lock", "owner"); !errors.Is(err, ErrCacheLockUnsupported) {
		t.Fatalf("无锁驱动 ReleaseLock 必须返回 ErrCacheLockUnsupported: %v", err)
	}
	if _, err = unsupported.RenewLock("lock", "owner", time.Minute); !errors.Is(err, ErrCacheLockUnsupported) {
		t.Fatalf("无续期能力必须返回 ErrCacheLockUnsupported: %v", err)
	}

	identifiedBackend := &identifiedClosingCacheDriver{Memory: cacheDriver.NewMemory(), identity: "redis://cache/0"}
	identified, err := NewNamespaceDriver(identifiedBackend, "tenant:", "")
	if err != nil {
		t.Fatalf("创建资源身份驱动失败: %v", err)
	}
	if identity := identified.CacheResourceIdentity(); !strings.HasPrefix(identity, "redis://cache/0|namespace:") {
		t.Fatalf("资源身份未包含逻辑命名空间: %q", identity)
	}
	if err = identified.Close(); err != nil || !identifiedBackend.closed {
		t.Fatalf("底层 Close 未透传: closed=%t err=%v", identifiedBackend.closed, err)
	}
	if _, err = NewNamespaceDriver(nil, "", ""); !errors.Is(err, ErrCacheDriverNotConfigured) {
		t.Fatalf("nil 底层驱动必须返回 ErrCacheDriverNotConfigured: %v", err)
	}
}

type identifiedClosingCacheDriver struct {
	*cacheDriver.Memory
	identity string
	closed   bool
}

func (driver *identifiedClosingCacheDriver) CacheResourceIdentity() string {
	return driver.identity
}

func (driver *identifiedClosingCacheDriver) Close() error {
	driver.closed = true
	return nil
}
