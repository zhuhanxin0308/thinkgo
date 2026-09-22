package cache

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
)

type pausedRememberDriver struct {
	*cacheDriver.Memory
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	lockKey string
	owner   string
}

func (driver *pausedRememberDriver) AcquireLock(key, owner string, ttl time.Duration) (bool, error) {
	driver.lockKey, driver.owner = key, owner
	return driver.Memory.AcquireLock(key, owner, ttl)
}

func (driver *pausedRememberDriver) Get(key string) (interface{}, bool, error) {
	if strings.HasPrefix(key, tagReversePrefix) && driver.armed.Load() {
		driver.once.Do(func() { close(driver.entered); <-driver.release })
	}
	return driver.Memory.Get(key)
}

// TestRememberLostOwnerCannotOverwriteSuccessor 暂停最后续租后的写入，验证新 owner 的结果不会被迟到加载覆盖。
func TestRememberLostOwnerCannotOverwriteSuccessor(t *testing.T) {
	const leaseTTL = time.Minute
	const waitBudget = 3 * time.Second
	backend := &pausedRememberDriver{Memory: cacheDriver.NewMemory(), entered: make(chan struct{}), release: make(chan struct{})}
	first, second := NewCache(nil, backend), NewCache(nil, backend.Memory)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(backend.release) }) }
	t.Cleanup(release)
	result := make(chan error, 1)
	go func() {
		_, err := first.RememberWithLock("resource", leaseTTL, leaseTTL, func() (interface{}, error) {
			backend.armed.Store(true)
			return "old", nil
		})
		result <- err
	}()
	select {
	case <-backend.entered:
	case <-time.After(waitBudget):
		t.Fatal("旧写入未到达暂停点")
	}
	if released, err := backend.Memory.ReleaseLock(backend.lockKey, backend.owner); err != nil || !released {
		t.Fatalf("模拟租约丢失失败: %v", err)
	}
	if _, err := second.RememberWithLock("resource", leaseTTL, leaseTTL, func() (interface{}, error) { return "new", nil }); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCacheLockLost) {
			t.Fatalf("旧 owner 未报告失效: %v", err)
		}
	case <-time.After(waitBudget):
		t.Fatal("旧 owner 未退出")
	}
	value, found, err := second.Get("resource")
	if err != nil || !found || value != "new" {
		t.Fatalf("新 owner 的结果被覆盖: value=%v found=%t err=%v", value, found, err)
	}
}

// legacyGuardlessDriver 模拟支持锁和续租、但尚未实现原子租约提交的既有驱动。
type legacyGuardlessDriver struct {
	Driver
	DistributedLocker
	LockRenewer
}

// TestRememberRejectsGuardlessDriverBeforeLoading 验证不安全驱动在业务回调前拒绝写回，基础锁 API 继续可用。
func TestRememberRejectsGuardlessDriverBeforeLoading(t *testing.T) {
	for _, namespaced := range []bool{false, true} {
		memory := cacheDriver.NewMemory()
		var backend Driver = &legacyGuardlessDriver{Driver: memory, DistributedLocker: memory, LockRenewer: memory}
		if namespaced {
			wrapped, err := NewNamespaceDriver(backend, "tenant:", "")
			if err != nil {
				t.Fatal(err)
			}
			backend = wrapped
		}
		manager := NewCache(nil, backend)
		called := false
		_, err := manager.RememberWithLock("value", time.Minute, time.Minute, func() (interface{}, error) { called = true; return "unsafe", nil })
		if !errors.Is(err, ErrCacheLockUnsupported) || called {
			t.Fatalf("缺少原子提交时执行了加载: namespace=%t called=%t err=%v", namespaced, called, err)
		}
		lock, err := manager.Lock("manual", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := lock.Acquire(); err != nil || !ok {
			t.Fatalf("基础锁兼容性损坏: %t %v", ok, err)
		}
		if ok, err := lock.Release(); err != nil || !ok {
			t.Fatalf("基础锁无法释放: %t %v", ok, err)
		}
	}
}
