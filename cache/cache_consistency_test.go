package cache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
)

// blockingDeleteDriver 只阻塞指定业务键的删除，用于稳定复现标签失效与并发写入的交错。
type blockingDeleteDriver struct {
	*cacheDriver.Memory
	target  string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

// leaseLostFencingDriver 模拟标签锁已经失效：删除在进入驱动原子边界前暂停，
// 让新的写入能够跨管理器成功提交，用于验证代际而非租约本身提供最终保护。
type leaseLostFencingDriver struct {
	*cacheDriver.Memory
	target  string
	block   atomic.Bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*leaseLostFencingDriver) supportsTagMutationLock() bool { return false }

func (driver *leaseLostFencingDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if key == driver.target && driver.block.CompareAndSwap(true, false) {
		driver.once.Do(func() { close(driver.started) })
		<-driver.release
	}
	return driver.Memory.Update(key, ttl, update)
}

func (driver *blockingDeleteDriver) Delete(key string) error {
	if key == driver.target {
		driver.once.Do(func() { close(driver.started) })
		<-driver.release
	}
	return driver.Memory.Delete(key)
}

func (driver *blockingDeleteDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return driver.Memory.Update(key, ttl, func(value interface{}, found bool) (interface{}, bool, error) {
		next, remove, err := update(value, found)
		if err == nil && remove && key == driver.target {
			driver.once.Do(func() { close(driver.started) })
			<-driver.release
		}
		return next, remove, err
	})
}

// TestTaggedFlushFencesConcurrentPlainSet 验证标签失效开始后，普通写入必须等待失效完成，
// 已成功返回的新值不能再被旧一代 Flush 删除。
func TestTaggedFlushFencesConcurrentPlainSet(t *testing.T) {
	backend := &blockingDeleteDriver{
		Memory:  cacheDriver.NewMemory(),
		target:  "profile:1",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	invalidator := NewCache(nil, backend)
	writer := NewCache(nil, backend)
	tagged, err := invalidator.Tag("profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set("profile:1", "old", time.Minute); err != nil {
		t.Fatalf("准备旧缓存值失败: %v", err)
	}

	flushDone := make(chan error, 1)
	go func() { flushDone <- tagged.Flush() }()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("标签 Flush 未进入业务键删除阶段")
	}

	setDone := make(chan error, 1)
	go func() { setDone <- writer.Set("profile:1", "new", time.Minute) }()
	select {
	case setErr := <-setDone:
		close(backend.release)
		<-flushDone
		t.Fatalf("失效仍在进行时新写入不应提前成功: %v", setErr)
	case <-time.After(30 * time.Millisecond):
	}
	close(backend.release)
	if err = <-flushDone; err != nil {
		t.Fatalf("标签 Flush 失败: %v", err)
	}
	if err = <-setDone; err != nil {
		t.Fatalf("失效完成后的新写入失败: %v", err)
	}
	value, found, err := writer.Get("profile:1")
	if err != nil || !found || value != "new" {
		t.Fatalf("并发新值被旧失效删除: value=%#v found=%t err=%v", value, found, err)
	}
}

// TestTaggedFlushGenerationPreservesWriteAfterInvalidationStart 验证即使分布式锁租约已丢失，
// 失效开始后拿到更高代际并成功返回的写入也不会被旧 Flush 删除。
func TestTaggedFlushGenerationPreservesWriteAfterInvalidationStart(t *testing.T) {
	backend := &leaseLostFencingDriver{
		Memory:  cacheDriver.NewMemory(),
		target:  "profile:lease-lost",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	invalidator := NewCache(nil, backend)
	writer := NewCache(nil, backend)
	tagged, err := invalidator.Tag("profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set(backend.target, "old", time.Minute); err != nil {
		t.Fatalf("准备旧缓存值失败: %v", err)
	}
	backend.block.Store(true)
	flushDone := make(chan error, 1)
	go func() { flushDone <- tagged.Flush() }()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("旧 Flush 未进入暂停的原子删除")
	}
	if err = writer.Set(backend.target, "new", time.Minute); err != nil {
		close(backend.release)
		<-flushDone
		t.Fatalf("失效开始后的新一代写入失败: %v", err)
	}
	close(backend.release)
	if err = <-flushDone; err != nil {
		t.Fatalf("旧 Flush 失败: %v", err)
	}
	value, found, err := writer.Get(backend.target)
	if err != nil || !found || value != "new" {
		t.Fatalf("旧 Flush 删除了成功的新一代值: value=%#v found=%t err=%v", value, found, err)
	}
}

// TestCacheMissRepairsExpiredTagMetadata 验证普通读取观察到自然过期后，也会清理键与标签的双向元数据。
func TestCacheMissRepairsExpiredTagMetadata(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set("profile:expired", "value", 10*time.Millisecond); err != nil {
		t.Fatalf("写入短期标签缓存失败: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, found, getErr := manager.Get("profile:expired"); getErr != nil || found {
		t.Fatalf("过期缓存读取错误: found=%t err=%v", found, getErr)
	}
	for _, metadataKey := range []string{manager.tagMetaKey("profiles"), manager.tagReverseKey("profile:expired")} {
		if value, found, getErr := backend.Get(metadataKey); getErr != nil || found {
			t.Fatalf("过期后残留标签元数据 %q: value=%#v found=%t err=%v", metadataKey, value, found, getErr)
		}
	}
}

// TestCacheBatchMissRepairsExpiredTagMetadata 验证批量读取未命中也会最终清理双向标签关系。
func TestCacheBatchMissRepairsExpiredTagMetadata(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	for _, key := range []string{"profile:batch-1", "profile:batch-2"} {
		if err = tagged.Set(key, "value", 10*time.Millisecond); err != nil {
			t.Fatalf("写入短期批量缓存失败: key=%s err=%v", key, err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	values, err := manager.GetMany([]string{"profile:batch-1", "profile:batch-2"})
	if err != nil || len(values) != 0 {
		t.Fatalf("批量读取过期缓存错误: values=%#v err=%v", values, err)
	}
	for _, metadataKey := range []string{
		manager.tagMetaKey("profiles"),
		manager.tagReverseKey("profile:batch-1"),
		manager.tagReverseKey("profile:batch-2"),
	} {
		if _, found, getErr := backend.Get(metadataKey); getErr != nil || found {
			t.Fatalf("批量过期后残留标签元数据 %q: found=%t err=%v", metadataKey, found, getErr)
		}
	}
}

// TestTaggedFlushRepairsForwardGhost 验证仅剩正向成员的幽灵关系会被回收，且不会被误判为可删除的业务值。
func TestTaggedFlushRepairsForwardGhost(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = backend.Set(manager.tagMetaKey("profiles"), []string{"profile:ghost"}, 0); err != nil {
		t.Fatalf("准备正向幽灵元数据失败: %v", err)
	}
	if err = tagged.Flush(); err != nil {
		t.Fatalf("清理正向幽灵元数据失败: %v", err)
	}
	if _, found, getErr := backend.Get(manager.tagMetaKey("profiles")); getErr != nil || found {
		t.Fatalf("正向幽灵元数据仍然存在: found=%t err=%v", found, getErr)
	}
}

// TestTaggedFlushRepairsLiveForwardGhostWithoutDeletingValue 验证单边正向关系即使指向现存键，
// 也只能清理元数据，不能把缺少反向证明的业务值当作标签成员删除。
func TestTaggedFlushRepairsLiveForwardGhostWithoutDeletingValue(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = backend.Set("profile:live", "keep", 0); err != nil {
		t.Fatalf("准备业务值失败: %v", err)
	}
	if err = backend.Set(manager.tagMetaKey("profiles"), []string{"profile:live"}, 0); err != nil {
		t.Fatalf("准备现存键正向幽灵失败: %v", err)
	}
	if err = tagged.Flush(); err != nil {
		t.Fatalf("清理现存键正向幽灵失败: %v", err)
	}
	if value, found, getErr := backend.Get("profile:live"); getErr != nil || !found || value != "keep" {
		t.Fatalf("正向幽灵清理误删业务值: value=%#v found=%t err=%v", value, found, getErr)
	}
	if _, found, getErr := backend.Get(manager.tagMetaKey("profiles")); getErr != nil || found {
		t.Fatalf("正向幽灵元数据仍然存在: found=%t err=%v", found, getErr)
	}
}

// TestForgetRepairsReverseGhost 验证仅剩反向关系时，显式删除仍可最终清理元数据。
func TestForgetRepairsReverseGhost(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	reverseKey := manager.tagReverseKey("profile:ghost")
	if err := backend.Set(reverseKey, []string{"profiles"}, 0); err != nil {
		t.Fatalf("准备反向幽灵元数据失败: %v", err)
	}
	if err := manager.Forget("profile:ghost"); err != nil {
		t.Fatalf("清理反向幽灵元数据失败: %v", err)
	}
	if _, found, err := backend.Get(reverseKey); err != nil || found {
		t.Fatalf("反向幽灵元数据仍然存在: found=%t err=%v", found, err)
	}
}

// TestTaggedFlushStillRejectsInvalidForwardMembers 保留安全边界：幽灵修复不能接受内部保留键注入。
func TestTaggedFlushStillRejectsInvalidForwardMembers(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = backend.Set(manager.tagMetaKey("profiles"), []string{tagMetaPrefix + "poison"}, 0); err != nil {
		t.Fatalf("准备非法元数据失败: %v", err)
	}
	if err = tagged.Flush(); !errors.Is(err, ErrCorruptTagMetadata) {
		t.Fatalf("非法正向成员必须继续失败关闭: %v", err)
	}
}
