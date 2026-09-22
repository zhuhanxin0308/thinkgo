package cache

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
	redisDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver/redis"
	"github.com/zhuhanxin0308/thinkgo/framework/debug"
)

// TestCacheStoresTagsAndLocks 验证多存储、标签、计数和锁的成功路径均保留明确错误边界。
func TestCacheStoresTagsAndLocks(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	if err := manager.RegisterStore("secondary", cacheDriver.NewMemory()); err != nil {
		t.Fatalf("注册 secondary store 失败: %v", err)
	}
	if err := manager.Set("counter", int64(1), 0); err != nil {
		t.Fatalf("写入计数缓存失败: %v", err)
	}
	if value, err := manager.Inc("counter", 2); err != nil || value != 3 {
		t.Fatalf("Inc 结果不正确: value=%d err=%v", value, err)
	}
	if value, err := manager.Dec("counter", 1); err != nil || value != 2 {
		t.Fatalf("Dec 结果不正确: value=%d err=%v", value, err)
	}

	secondary, err := manager.Store("secondary")
	if err != nil {
		t.Fatalf("选择 secondary store 失败: %v", err)
	}
	if err = secondary.Set("profile", "secondary-store", 0); err != nil {
		t.Fatalf("写入 secondary store 失败: %v", err)
	}
	if _, found, err := manager.Get("profile"); err != nil || found {
		t.Fatalf("默认 store 不应读到 secondary 数据: found=%t err=%v", found, err)
	}
	if value, found, err := secondary.Get("profile"); err != nil || !found || value != "secondary-store" {
		t.Fatalf("secondary store 读取失败: value=%#v found=%t err=%v", value, found, err)
	}

	tagged, err := manager.Tag("users", "profile", "users")
	if err != nil {
		t.Fatalf("创建标签缓存失败: %v", err)
	}
	if err = tagged.Set("user:1", "张三", time.Minute); err != nil {
		t.Fatalf("标签缓存写入失败: %v", err)
	}
	if value, found, err := manager.Get("user:1"); err != nil || !found || value != "张三" {
		t.Fatalf("标签写入未落到缓存: value=%#v found=%t err=%v", value, found, err)
	}
	users, err := manager.Tag("users")
	if err != nil {
		t.Fatalf("创建 users 标签视图失败: %v", err)
	}
	if err = users.Flush(); err != nil {
		t.Fatalf("清理 users 标签失败: %v", err)
	}
	if _, found, err := manager.Get("user:1"); err != nil || found {
		t.Fatalf("标签清理后缓存仍存在: found=%t err=%v", found, err)
	}

	lockA, err := manager.Lock("sync-job", time.Second)
	if err != nil {
		t.Fatalf("创建第一个锁失败: %v", err)
	}
	acquired, err := lockA.Acquire()
	if err != nil || !acquired {
		t.Fatalf("第一个锁应获取成功: acquired=%t err=%v", acquired, err)
	}
	lockB, err := manager.Lock("sync-job", time.Second)
	if err != nil {
		t.Fatalf("创建第二个锁失败: %v", err)
	}
	if acquired, err = lockB.Acquire(); err != nil || acquired {
		t.Fatalf("未释放前第二个锁不应获取成功: acquired=%t err=%v", acquired, err)
	}
	if released, err := lockA.Release(); err != nil || !released {
		t.Fatalf("第一个锁释放失败: released=%t err=%v", released, err)
	}
	if acquired, err = lockB.Acquire(); err != nil || !acquired {
		t.Fatalf("释放后第二个锁应获取成功: acquired=%t err=%v", acquired, err)
	}
}

// TestRegisterStoreRejectsDistinctDriversSharingResource 验证运行时注册路径也不会让两个 store 复用同一后端 namespace。
func TestRegisterStoreRejectsDistinctDriversSharingResource(t *testing.T) {
	tests := []struct {
		name   string
		create func(t *testing.T) (Driver, Driver)
	}{
		{
			name: "file",
			create: func(t *testing.T) (Driver, Driver) {
				path := filepath.Join(t.TempDir(), "cache")
				first, err := cacheDriver.NewFile(path)
				if err != nil {
					t.Fatalf("创建第一个文件驱动失败: %v", err)
				}
				second, err := cacheDriver.NewFile(path)
				if err != nil {
					t.Fatalf("创建第二个文件驱动失败: %v", err)
				}
				return first, second
			},
		},
		{
			name: "redis",
			create: func(t *testing.T) (Driver, Driver) {
				config := map[string]interface{}{"host": "localhost", "port": 6379, "select": 2, "prefix": "shared:"}
				first, err := redisDriver.NewRedis(config)
				if err != nil {
					t.Fatalf("创建第一个 Redis 驱动失败: %v", err)
				}
				second, err := redisDriver.NewRedis(config)
				if err != nil {
					_ = first.Close()
					t.Fatalf("创建第二个 Redis 驱动失败: %v", err)
				}
				t.Cleanup(func() {
					_ = first.Close()
					_ = second.Close()
				})
				return first, second
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first, second := test.create(t)
			manager := NewCache(nil, first)
			if err := manager.RegisterStore("secondary", second); !errors.Is(err, ErrCacheStoreExists) {
				t.Fatalf("复用同一后端的不同驱动应被拒绝，实际错误为 %v", err)
			}
		})
	}
}

// TestCacheLockCanRenew 验证持有者可以在长任务中延长锁租约，并且不会修改 owner 边界。
func TestCacheLockCanRenew(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	lock, err := manager.Lock("lease", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("创建租约锁失败: %v", err)
	}
	if acquired, err := lock.Acquire(); err != nil || !acquired {
		t.Fatalf("获取租约锁失败: acquired=%t err=%v", acquired, err)
	}
	if renewed, err := lock.Renew(time.Minute); err != nil || !renewed {
		t.Fatalf("续租失败: renewed=%t err=%v", renewed, err)
	}
	other, err := manager.Lock("lease", time.Minute)
	if err != nil {
		t.Fatalf("创建竞争租约锁失败: %v", err)
	}
	if acquired, err := other.Acquire(); err != nil || acquired {
		t.Fatalf("续租后竞争 owner 不应获取锁: acquired=%t err=%v", acquired, err)
	}
	if released, err := lock.Release(); err != nil || !released {
		t.Fatalf("释放续租锁失败: released=%t err=%v", released, err)
	}
}

// TestTaggedCacheCapacityFailurePreservesMetadata 验证有界内存标签缓存容量不足时不会留下损坏关系。
func TestTaggedCacheCapacityFailurePreservesMetadata(t *testing.T) {
	driver, err := cacheDriver.NewMemoryWithMaxEntries(3)
	if err != nil {
		t.Fatalf("创建有界内存缓存失败: %v", err)
	}
	manager := NewCache(nil, driver)
	tagged, err := manager.Tag("users")
	if err != nil {
		t.Fatalf("创建标签缓存失败: %v", err)
	}
	if err = tagged.Set("user:1", "张三", 0); err != nil {
		t.Fatalf("首个标签项写入失败: %v", err)
	}
	if err = tagged.Set("user:2", "李四", 0); !errors.Is(err, cacheDriver.ErrMemoryCapacityExhausted) {
		t.Fatalf("容量不足时标签写入应显式失败，实际错误为 %v", err)
	}
	if value, found, getErr := tagged.Get("user:1"); getErr != nil || !found || value != "张三" {
		t.Fatalf("标签写入失败不得破坏已有项: value=%#v found=%t err=%v", value, found, getErr)
	}
	if err = tagged.Flush(); err != nil {
		t.Fatalf("容量失败后的标签元数据仍应可清理: %v", err)
	}
	if _, found, getErr := manager.Get("user:1"); getErr != nil || found {
		t.Fatalf("标签清理后业务项不应继续存在: found=%t err=%v", found, getErr)
	}
}

// TestCacheDistinguishesNilHitAndCoalescesRemember 验证 nil 值仍是命中，且并发 Remember 只执行一次回调。
func TestCacheDistinguishesNilHitAndCoalescesRemember(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	if err := manager.Set("nil-value", nil, time.Minute); err != nil {
		t.Fatalf("写入 nil 缓存失败: %v", err)
	}
	value, found, err := manager.Get("nil-value")
	if err != nil || !found || value != nil {
		t.Fatalf("nil 缓存命中语义错误: value=%#v found=%t err=%v", value, found, err)
	}

	const workers = 32
	var callbackCount atomic.Int32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := manager.Remember("coalesced", time.Minute, func() (interface{}, error) {
				callbackCount.Add(1)
				time.Sleep(10 * time.Millisecond)
				return "computed", nil
			})
			if err != nil {
				errorsChannel <- err
				return
			}
			if value != "computed" {
				errorsChannel <- errors.New("Remember 返回值错误")
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("并发 Remember 失败: %v", err)
		}
	}
	if callbackCount.Load() != 1 {
		t.Fatalf("并发 Remember 回调只能执行一次，实际为 %d", callbackCount.Load())
	}
}

// TestRememberWithLockCoalescesAcrossCacheManagers 验证显式分布式 Remember 能跨管理器合并缓存未命中加载。
func TestRememberWithLockCoalescesAcrossCacheManagers(t *testing.T) {
	driver := cacheDriver.NewMemory()
	managerA := NewCache(nil, driver)
	managerB := NewCache(nil, driver)
	const workers = 24
	var callbackCount atomic.Int32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			manager := managerA
			if index%2 == 1 {
				manager = managerB
			}
			value, err := manager.RememberWithLock("distributed", time.Minute, time.Second, func() (interface{}, error) {
				callbackCount.Add(1)
				time.Sleep(20 * time.Millisecond)
				return "computed", nil
			})
			if err != nil || value != "computed" {
				errorsChannel <- fmt.Errorf("value=%#v err=%v", value, err)
			}
		}(index)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("跨管理器 RememberWithLock 失败: %v", err)
		}
	}
	if callbackCount.Load() != 1 {
		t.Fatalf("跨管理器 RememberWithLock 回调次数错误: %d", callbackCount.Load())
	}
}

// TestTaggedCacheHonorsCrossManagerMutationLock 验证标签写入不会绕过其它管理器持有的标签锁。
// TestRememberWithLockReleasesAfterPanic 验证 RememberWithLock 的回调 panic 也不会遗留活动锁。
func TestRememberWithLockReleasesAfterPanic(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("RememberWithLock 回调 panic 应继续向调用方传播")
			}
		}()
		_, _ = manager.RememberWithLock("panic", time.Minute, time.Second, func() (interface{}, error) {
			panic("load panic")
		})
	}()
	lock, err := manager.Lock("panic", time.Minute)
	if err != nil {
		t.Fatalf("创建 panic 回调复用锁失败: %v", err)
	}
	if acquired, err := lock.Acquire(); err != nil || !acquired {
		t.Fatalf("panic 回调结束后锁仍未释放: acquired=%t err=%v", acquired, err)
	}
	if released, err := lock.Release(); err != nil || !released {
		t.Fatalf("释放 panic 回调复用锁失败: released=%t err=%v", released, err)
	}
}

// TestRememberWithLockRenewsLease 验证长于初始租约的回调会自动续租并安全写回结果。
func TestRememberWithLockRenewsLease(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	value, err := manager.RememberWithLock("expired-load", time.Minute, 100*time.Millisecond, func() (interface{}, error) {
		time.Sleep(250 * time.Millisecond)
		return "stale", nil
	})
	if value != "stale" || err != nil {
		t.Fatalf("自动续租后应返回回调值且不丢锁: value=%#v err=%v", value, err)
	}
	if cached, found, getErr := manager.Get("expired-load"); getErr != nil || !found || cached != "stale" {
		t.Fatalf("自动续租后应写入结果: cached=%#v found=%t err=%v", cached, found, getErr)
	}
}

// TestRememberWithLockRejectsNonRenewableLocker 验证仅实现基础锁接口的自定义驱动不会执行无法安全写回的回调。
func TestRememberWithLockRejectsNonRenewableLocker(t *testing.T) {
	driver := &distributedOnlyDriver{memory: cacheDriver.NewMemory()}
	manager := NewCache(nil, driver)
	callbackCalled := false
	if _, err := manager.RememberWithLock("non-renewable", time.Minute, time.Second, func() (interface{}, error) {
		callbackCalled = true
		return "value", nil
	}); !errors.Is(err, ErrCacheLockUnsupported) {
		t.Fatalf("不可续租驱动应在回调前返回 ErrCacheLockUnsupported，实际为 %v", err)
	}
	if callbackCalled {
		t.Fatal("不可续租驱动不应执行 RememberWithLock 回调")
	}
}

func TestTaggedCacheHonorsCrossManagerMutationLock(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	lock, err := manager.Lock("tag:"+defaultStoreName, time.Minute)
	if err != nil {
		t.Fatalf("创建标签互斥锁失败: %v", err)
	}
	if acquired, err := lock.Acquire(); err != nil || !acquired {
		t.Fatalf("获取标签互斥锁失败: acquired=%t err=%v", acquired, err)
	}
	tagged, err := manager.Tag("users")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set("user:1", "张三", time.Minute); !errors.Is(err, ErrCacheLockBusy) {
		t.Fatalf("标签写入应尊重活动互斥锁，实际错误为 %v", err)
	}
	if released, err := lock.Release(); err != nil || !released {
		t.Fatalf("释放标签互斥锁失败: released=%t err=%v", released, err)
	}
}

// TestCacheRejectsInvalidNamesAndMissingDriver 验证配置错误、保留键和缺失驱动不会静默回退。
func TestCacheRejectsInvalidNamesAndMissingDriver(t *testing.T) {
	manager := NewCache(nil, nil)
	if _, _, err := manager.Get("missing"); !errors.Is(err, ErrCacheDriverNotConfigured) {
		t.Fatalf("无驱动读取应返回 ErrCacheDriverNotConfigured，实际为 %v", err)
	}
	if err := manager.Set("missing", "value", time.Minute); !errors.Is(err, ErrCacheDriverNotConfigured) {
		t.Fatalf("无驱动写入应返回 ErrCacheDriverNotConfigured，实际为 %v", err)
	}
	if _, err := manager.Store("missing"); !errors.Is(err, ErrCacheStoreNotFound) {
		t.Fatalf("未知 store 应返回 ErrCacheStoreNotFound，实际为 %v", err)
	}
	if err := manager.RegisterStore("bad name", cacheDriver.NewMemory()); !errors.Is(err, ErrInvalidCacheStore) {
		t.Fatalf("非法 store 名应被拒绝，实际为 %v", err)
	}
	if err := manager.Set(tagMetaPrefix+"users", "collision", 0); !errors.Is(err, ErrReservedCacheKey) {
		t.Fatalf("内部保留键应被拒绝，实际为 %v", err)
	}
	if _, err := manager.Tag("", "users"); !errors.Is(err, ErrInvalidCacheTag) {
		t.Fatalf("空标签应被拒绝，实际为 %v", err)
	}
	if _, err := manager.Lock("job", -time.Second); !errors.Is(err, ErrInvalidCacheTTL) {
		t.Fatalf("负锁 TTL 应被拒绝，实际为 %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("关闭无驱动缓存失败: %v", err)
	}
	if _, _, err := manager.Get("after-close"); !errors.Is(err, ErrCacheClosed) {
		t.Fatalf("关闭后操作应返回 ErrCacheClosed，实际为 %v", err)
	}
}

type failingDriver struct {
	err error
}

func (d *failingDriver) Get(string) (interface{}, bool, error)        { return nil, false, d.err }
func (d *failingDriver) Set(string, interface{}, time.Duration) error { return d.err }
func (d *failingDriver) Has(string) (bool, error)                     { return false, d.err }
func (d *failingDriver) Delete(string) error                          { return d.err }
func (d *failingDriver) Clear() error                                 { return d.err }
func (d *failingDriver) Inc(string, int64) (int64, error)             { return 0, d.err }
func (d *failingDriver) Dec(string, int64) (int64, error)             { return 0, d.err }

type distributedOnlyDriver struct {
	memory *cacheDriver.Memory
}

func (d *distributedOnlyDriver) Get(key string) (interface{}, bool, error) {
	return d.memory.Get(key)
}

func (d *distributedOnlyDriver) Set(key string, value interface{}, ttl time.Duration) error {
	return d.memory.Set(key, value, ttl)
}

func (d *distributedOnlyDriver) Has(key string) (bool, error) {
	return d.memory.Has(key)
}

func (d *distributedOnlyDriver) Delete(key string) error {
	return d.memory.Delete(key)
}

func (d *distributedOnlyDriver) Clear() error {
	return d.memory.Clear()
}

func (d *distributedOnlyDriver) Inc(key string, step int64) (int64, error) {
	return d.memory.Inc(key, step)
}

func (d *distributedOnlyDriver) Dec(key string, step int64) (int64, error) {
	return d.memory.Dec(key, step)
}

func (d *distributedOnlyDriver) AcquireLock(key, owner string, ttl time.Duration) (bool, error) {
	return d.memory.AcquireLock(key, owner, ttl)
}

func (d *distributedOnlyDriver) ReleaseLock(key, owner string) (bool, error) {
	return d.memory.ReleaseLock(key, owner)
}

// TestCachePropagatesDriverAndCallbackErrors 验证驱动错误与 Remember 回调错误原样向上传播。
func TestCachePropagatesDriverAndCallbackErrors(t *testing.T) {
	driverErr := errors.New("cache backend unavailable")
	manager := NewCache(nil, &failingDriver{err: driverErr})
	if _, _, err := manager.Get("key"); !errors.Is(err, driverErr) {
		t.Fatalf("Get 应传播驱动错误，实际为 %v", err)
	}
	if err := manager.Set("key", "value", time.Minute); !errors.Is(err, driverErr) {
		t.Fatalf("Set 应传播驱动错误，实际为 %v", err)
	}
	if _, err := manager.Inc("key", 1); !errors.Is(err, driverErr) {
		t.Fatalf("Inc 应传播驱动错误，实际为 %v", err)
	}

	callbackErr := errors.New("load failed")
	memoryManager := NewCache(nil, cacheDriver.NewMemory())
	if _, err := memoryManager.Remember("key", time.Minute, func() (interface{}, error) {
		return nil, callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("Remember 应传播回调错误，实际为 %v", err)
	}
	if _, found, err := memoryManager.Get("key"); err != nil || found {
		t.Fatalf("回调失败不得写入缓存: found=%t err=%v", found, err)
	}
}

type oneShotSetFailureDriver struct {
	*cacheDriver.Memory
	mu      sync.Mutex
	failKey string
	err     error
	failed  bool
}

func (d *oneShotSetFailureDriver) Set(key string, value interface{}, ttl time.Duration) error {
	d.mu.Lock()
	if key == d.failKey && !d.failed {
		d.failed = true
		d.mu.Unlock()
		return d.err
	}
	d.mu.Unlock()
	return d.Memory.Set(key, value, ttl)
}

// TestTaggedSetRollsBackPartialMetadata 验证标签双向元数据任一步骤失败时不会留下幽灵成员。
func TestTaggedSetRollsBackPartialMetadata(t *testing.T) {
	backend := cacheDriver.NewMemory()
	driverErr := errors.New("reverse metadata unavailable")
	failing := &oneShotSetFailureDriver{Memory: backend, err: driverErr}
	manager := NewCache(nil, failing)
	failing.failKey = manager.tagReverseKey("user:1")
	tagged, err := manager.Tag("users", "profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set("user:1", "张三", time.Minute); !errors.Is(err, driverErr) {
		t.Fatalf("反向元数据失败应向上传播，实际为 %v", err)
	}
	for _, key := range []string{
		"user:1",
		manager.tagMetaKey("users"),
		manager.tagMetaKey("profiles"),
		manager.tagReverseKey("user:1"),
	} {
		if _, found, getErr := backend.Get(key); getErr != nil || found {
			t.Fatalf("失败回滚后仍残留键 %q: found=%t err=%v", key, found, getErr)
		}
	}
}

// TestTaggedFlushRejectsPoisonedMetadata 验证损坏元数据不能诱导标签清理删除内部键或业务键。
func TestTaggedFlushRejectsPoisonedMetadata(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	tagged, err := manager.Tag("users")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = backend.Set(manager.tagMetaKey("users"), []interface{}{tagMetaPrefix + "poison"}, 0); err != nil {
		t.Fatalf("注入损坏元数据失败: %v", err)
	}
	if err = backend.Set("safe", "value", 0); err != nil {
		t.Fatalf("写入安全业务键失败: %v", err)
	}
	if err = tagged.Flush(); !errors.Is(err, ErrCorruptTagMetadata) {
		t.Fatalf("损坏元数据应拒绝清理，实际为 %v", err)
	}
	if value, found, getErr := backend.Get("safe"); getErr != nil || !found || value != "value" {
		t.Fatalf("拒绝清理时业务数据被改动: value=%#v found=%t err=%v", value, found, getErr)
	}
}

type closingDriver struct {
	*cacheDriver.Memory
	count atomic.Int32
	err   error
}

func (d *closingDriver) Close() error {
	d.count.Add(1)
	return d.err
}

// TestCacheCloseIsIdempotentAndClosesUniqueDrivers 验证共享驱动只关闭一次，关闭错误稳定返回且状态不可逆。
func TestCacheCloseIsIdempotentAndClosesUniqueDrivers(t *testing.T) {
	closeErr := errors.New("close failed")
	driver := &closingDriver{Memory: cacheDriver.NewMemory(), err: closeErr}
	manager := NewCache(nil, driver)
	if err := manager.RegisterStore("secondary", driver); err != nil {
		t.Fatalf("注册共享驱动失败: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := manager.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("第 %d 次关闭未返回驱动错误: %v", attempt+1, err)
		}
	}
	if driver.count.Load() != 1 {
		t.Fatalf("共享驱动应只关闭一次，实际为 %d", driver.count.Load())
	}
	if _, err := manager.Tag("users"); !errors.Is(err, ErrCacheClosed) {
		t.Fatalf("关闭后不得创建标签视图，实际为 %v", err)
	}
}

// TestCacheRejectsExcessiveTagFanout 验证单键标签上限在写入前即被拒绝。
func TestCacheRejectsExcessiveTagFanout(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	tags := make([]string, maxTagsPerCacheKey+1)
	for index := range tags {
		tags[index] = fmt.Sprintf("tag-%d", index)
	}
	if _, err := manager.Tag(tags...); !errors.Is(err, ErrInvalidCacheTag) {
		t.Fatalf("超量标签应返回 ErrInvalidCacheTag，实际为 %v", err)
	}
}

// TestCacheConvenienceAPIsAndTaggedCleanup 验证便捷 API、标签视图和普通 Forget 的双向元数据清理。
func TestCacheConvenienceAPIsAndTaggedCleanup(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	if err := manager.Forever("forever", "value"); err != nil {
		t.Fatalf("Forever 写入失败: %v", err)
	}
	if exists, err := manager.Has("forever"); err != nil || !exists {
		t.Fatalf("Has 未识别 Forever 值: exists=%t err=%v", exists, err)
	}
	tagged, err := manager.Tag("users", "profiles")
	if err != nil {
		t.Fatalf("创建标签视图失败: %v", err)
	}
	if err = tagged.Set("user:1", "张三", 0); err != nil {
		t.Fatalf("标签写入失败: %v", err)
	}
	if value, found, getErr := tagged.Get("user:1"); getErr != nil || !found || value != "张三" {
		t.Fatalf("标签 Get 错误: value=%#v found=%t err=%v", value, found, getErr)
	}
	if exists, hasErr := tagged.Has("user:1"); hasErr != nil || !exists {
		t.Fatalf("标签 Has 错误: exists=%t err=%v", exists, hasErr)
	}
	if err = manager.Forget("user:1"); err != nil {
		t.Fatalf("普通 Forget 清理标签键失败: %v", err)
	}
	for _, key := range []string{
		manager.tagMetaKey("users"), manager.tagMetaKey("profiles"), manager.tagReverseKey("user:1"),
	} {
		if _, found, getErr := backend.Get(key); getErr != nil || found {
			t.Fatalf("普通 Forget 后仍残留标签元数据 %q: found=%t err=%v", key, found, getErr)
		}
	}
	if err = tagged.Set("user:2", "李四", 0); err != nil {
		t.Fatalf("第二次标签写入失败: %v", err)
	}
	if err = tagged.Forget("user:2"); err != nil {
		t.Fatalf("标签 Forget 失败: %v", err)
	}
	if err = tagged.Set("expiring", "短值", 10*time.Millisecond); err != nil {
		t.Fatalf("写入短期标签缓存失败: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, found, getErr := tagged.Get("expiring"); getErr != nil || found {
		t.Fatalf("过期标签缓存应未命中: found=%t err=%v", found, getErr)
	}
	if _, found, getErr := backend.Get(manager.tagReverseKey("expiring")); getErr != nil || found {
		t.Fatalf("标签未命中后仍残留反向元数据: found=%t err=%v", found, getErr)
	}

	lock, err := manager.Lock("flush-guard", time.Minute)
	if err != nil {
		t.Fatalf("创建 Flush 锁失败: %v", err)
	}
	if acquired, acquireErr := lock.Acquire(); acquireErr != nil || !acquired {
		t.Fatalf("获取 Flush 锁失败: acquired=%t err=%v", acquired, acquireErr)
	}
	if err = manager.Flush(); err != nil {
		t.Fatalf("Flush 失败: %v", err)
	}
	if exists, hasErr := manager.Has("forever"); hasErr != nil || exists {
		t.Fatalf("Flush 后普通缓存仍存在: exists=%t err=%v", exists, hasErr)
	}
	if released, releaseErr := lock.Release(); releaseErr != nil || !released {
		t.Fatalf("Flush 不得释放活动锁: released=%t err=%v", released, releaseErr)
	}
}

// TestCacheValidationAndUnsupportedLockDriver 验证所有公开输入边界和不支持锁的驱动都返回稳定错误。
func TestCacheValidationAndUnsupportedLockDriver(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	invalidKeys := []string{"", "line\nbreak", "unicode\u0085control", strings.Repeat("k", maxCacheKeyBytes+1), lockKeyPrefix + "internal"}
	for _, key := range invalidKeys {
		if err := manager.Set(key, "value", 0); err == nil {
			t.Fatalf("非法缓存键 %q 不应写入成功", key)
		}
	}
	if err := manager.Set("key", "value", -time.Second); !errors.Is(err, ErrInvalidCacheTTL) {
		t.Fatalf("负缓存 TTL 应返回 ErrInvalidCacheTTL，实际为 %v", err)
	}
	if err := manager.Set("key", "value", maxCacheTTL+time.Second); !errors.Is(err, ErrInvalidCacheTTL) {
		t.Fatalf("超长缓存 TTL 应返回 ErrInvalidCacheTTL，实际为 %v", err)
	}
	if _, err := manager.Inc("counter", -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负 Inc 步长应返回 ErrInvalidCounterStep，实际为 %v", err)
	}
	if _, err := manager.Dec("counter", -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负 Dec 步长应返回 ErrInvalidCounterStep，实际为 %v", err)
	}
	if _, err := manager.Remember("key", 0, nil); !errors.Is(err, ErrNilRememberCallback) {
		t.Fatalf("nil Remember 回调应返回 ErrNilRememberCallback，实际为 %v", err)
	}
	if _, err := manager.Store("bad name"); !errors.Is(err, ErrInvalidCacheStore) {
		t.Fatalf("非法 store 选择应返回 ErrInvalidCacheStore，实际为 %v", err)
	}
	var typedNil *cacheDriver.Memory
	if err := manager.RegisterStore("typed-nil", typedNil); !errors.Is(err, ErrInvalidCacheStore) {
		t.Fatalf("typed nil 驱动应返回 ErrInvalidCacheStore，实际为 %v", err)
	}
	registered := cacheDriver.NewMemory()
	if err := manager.RegisterStore("stable", registered); err != nil {
		t.Fatalf("首次注册 stable store 失败: %v", err)
	}
	if err := manager.RegisterStore("stable", registered); err != nil {
		t.Fatalf("同一驱动重复注册应幂等: %v", err)
	}
	if err := manager.RegisterStore("stable", cacheDriver.NewMemory()); !errors.Is(err, ErrCacheStoreExists) {
		t.Fatalf("不同驱动不得静默替换 store，实际为 %v", err)
	}
	for _, tag := range []string{" spaced ", "line\nbreak", strings.Repeat("t", maxCacheTagBytes+1)} {
		if _, err := manager.Tag(tag); !errors.Is(err, ErrInvalidCacheTag) {
			t.Fatalf("非法标签 %q 应返回 ErrInvalidCacheTag，实际为 %v", tag, err)
		}
	}
	if _, err := manager.Lock("job", maxLockTTL+time.Second); !errors.Is(err, ErrInvalidCacheTTL) {
		t.Fatalf("超长锁 TTL 应返回 ErrInvalidCacheTTL，实际为 %v", err)
	}
	unsupported := NewCache(nil, &failingDriver{err: nil})
	lock, err := unsupported.Lock("job", time.Second)
	if err != nil {
		t.Fatalf("创建不支持锁的驱动视图不应提前失败: %v", err)
	}
	if _, err = lock.Acquire(); !errors.Is(err, ErrCacheLockUnsupported) {
		t.Fatalf("不支持锁的驱动应返回 ErrCacheLockUnsupported，实际为 %v", err)
	}
}

// TestRememberPanicReleasesCoalescingState 验证加载回调 panic 后等待状态被清理，后续请求可以重新计算。
func TestRememberPanicReleasesCoalescingState(t *testing.T) {
	manager := NewCache(nil, cacheDriver.NewMemory())
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Remember 回调 panic 应继续向调用方传播")
			}
		}()
		_, _ = manager.Remember("panic-key", time.Minute, func() (interface{}, error) {
			panic("load panic")
		})
	}()
	value, err := manager.Remember("panic-key", time.Minute, func() (interface{}, error) {
		return "recovered", nil
	})
	if err != nil || value != "recovered" {
		t.Fatalf("panic 后 Remember 未恢复: value=%#v err=%v", value, err)
	}
}

// TestCachePropagatesAllDriverOperationErrors 验证便捷 API 不会遗漏删除、判断、清空和递减错误。
func TestCachePropagatesAllDriverOperationErrors(t *testing.T) {
	driverErr := errors.New("backend failed")
	manager := NewCache(nil, &failingDriver{err: driverErr})
	if _, err := manager.Has("key"); !errors.Is(err, driverErr) {
		t.Fatalf("Has 未传播驱动错误: %v", err)
	}
	if err := manager.Forget("key"); !errors.Is(err, driverErr) {
		t.Fatalf("Forget 未传播驱动错误: %v", err)
	}
	if err := manager.Flush(); !errors.Is(err, driverErr) {
		t.Fatalf("Flush 未传播驱动错误: %v", err)
	}
	if _, err := manager.Dec("key", 1); !errors.Is(err, driverErr) {
		t.Fatalf("Dec 未传播驱动错误: %v", err)
	}
}

type blockingCloseDriver struct {
	*cacheDriver.Memory
	started     chan struct{}
	release     chan struct{}
	closeCalled chan struct{}
	startOnce   sync.Once
	closeOnce   sync.Once
}

func (d *blockingCloseDriver) Get(key string) (interface{}, bool, error) {
	d.startOnce.Do(func() { close(d.started) })
	<-d.release
	return d.Memory.Get(key)
}

func (d *blockingCloseDriver) Close() error {
	d.closeOnce.Do(func() { close(d.closeCalled) })
	return nil
}

// TestCacheCloseWaitsForActiveDriverOperation 验证关闭不会与正在执行的后端调用并发释放资源。
func TestCacheCloseWaitsForActiveDriverOperation(t *testing.T) {
	driver := &blockingCloseDriver{
		Memory:      cacheDriver.NewMemory(),
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	if err := driver.Set("key", "value", 0); err != nil {
		t.Fatalf("准备阻塞读取数据失败: %v", err)
	}
	manager := NewCache(nil, driver)
	getDone := make(chan error, 1)
	go func() {
		_, _, err := manager.Get("key")
		getDone <- err
	}()
	<-driver.started
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case <-driver.closeCalled:
		close(driver.release)
		t.Fatal("活动 Get 返回前驱动已被关闭")
	case <-time.After(30 * time.Millisecond):
	}
	close(driver.release)
	if err := <-getDone; err != nil {
		t.Fatalf("活动 Get 返回错误: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("等待活动操作后关闭失败: %v", err)
	}
	select {
	case <-driver.closeCalled:
	default:
		t.Fatal("活动操作结束后未关闭驱动")
	}
}

// TestCacheWithDebugSharesStateAndIsolatesCollectors 验证请求 facade 共享驱动生命周期，但不会共享调试数据。
func TestCacheWithDebugSharesStateAndIsolatesCollectors(t *testing.T) {
	root := NewCache(nil, cacheDriver.NewMemory())
	if root.WithDebug(nil) != root {
		t.Fatal("未绑定 collector 时应复用根 facade，避免关闭 Trace 的请求产生额外分配")
	}
	if err := root.RegisterStore("secondary", cacheDriver.NewMemory()); err != nil {
		t.Fatalf("注册 secondary store 失败: %v", err)
	}
	collectorA := debug.NewRequestDebug(true)
	collectorB := debug.NewRequestDebug(true)
	requestA := root.WithDebug(collectorA)
	requestB := root.WithDebug(collectorB)

	if requestA == root || requestB == root || requestA.state != root.state || requestB.state != root.state {
		t.Fatal("WithDebug 应复制轻量 facade 并共享同一底层状态")
	}
	if root.debug != nil || requestA.debug != collectorA || requestB.debug != collectorB {
		t.Fatal("root 应保持无 collector，派生 facade 应绑定各自请求 collector")
	}

	if err := requestA.Set("request-a", "shared", 0); err != nil {
		t.Fatalf("请求 A 写缓存失败: %v", err)
	}
	if value, found, err := requestB.Get("request-a"); err != nil || !found || value != "shared" {
		t.Fatalf("请求 facade 应共享驱动数据: value=%#v found=%t err=%v", value, found, err)
	}
	if err := root.Set("root-key", true, 0); err != nil {
		t.Fatalf("root 写缓存失败: %v", err)
	}

	secondaryA, err := requestA.Store("secondary")
	if err != nil {
		t.Fatalf("请求 A 选择 secondary store 失败: %v", err)
	}
	if secondaryA.debug != collectorA || secondaryA.state != root.state {
		t.Fatal("Store 派生 facade 应继续传播当前请求 collector 和共享状态")
	}
	if err = secondaryA.Set("secondary-a", true, 0); err != nil {
		t.Fatalf("请求 A secondary 写入失败: %v", err)
	}

	taggedB, err := requestB.Tag("request-b")
	if err != nil {
		t.Fatalf("请求 B 创建标签 facade 失败: %v", err)
	}
	if taggedB.cache.debug != collectorB || taggedB.cache.state != root.state {
		t.Fatal("Tag 派生 facade 应继续传播当前请求 collector 和共享状态")
	}
	if err = taggedB.Set("tagged-b", true, 0); err != nil {
		t.Fatalf("请求 B 标签写入失败: %v", err)
	}

	keysA := debugCacheKeys(collectorA)
	keysB := debugCacheKeys(collectorB)
	if !keysA["request-a"] || !keysA["secondary-a"] || keysA["tagged-b"] || keysA["root-key"] {
		t.Fatalf("请求 A 调试记录隔离错误: %#v", keysA)
	}
	if !keysB["request-a"] || !keysB["tagged-b"] || keysB["secondary-a"] || keysB["root-key"] {
		t.Fatalf("请求 B 调试记录隔离错误: %#v", keysB)
	}
}

// TestCacheWithDebugPreservesLegacyConstructorBinding 验证旧 NewCache trace 参数仍绑定直接调用 facade。
func TestCacheWithDebugPreservesLegacyConstructorBinding(t *testing.T) {
	collector := debug.NewRequestDebug(true)
	manager := NewCache(collector, cacheDriver.NewMemory())
	if err := manager.Set("legacy", true, 0); err != nil {
		t.Fatalf("旧构造方式写缓存失败: %v", err)
	}
	if keys := debugCacheKeys(collector); !keys["legacy"] {
		t.Fatalf("旧构造参数应继续记录调试数据，实际为 %#v", keys)
	}
}

func debugCacheKeys(collector *debug.Debug) map[string]bool {
	keys := make(map[string]bool)
	for _, entry := range collector.GetInfo()["cache"].([]map[string]interface{}) {
		key, _ := entry["key"].(string)
		keys[key] = true
	}
	return keys
}
