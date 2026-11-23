package cache

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 旧命名空间只断言直接下层的同步锁，实际取得、续期和释放仍优先走上下文接口。
type legacyTagLeaseNamespace struct{ *NamespaceDriver }

func (driver legacyTagLeaseNamespace) supportsTagMutationLock() bool {
	_, supported := driver.driver.(DistributedLocker)
	return supported
}

type contextTagLeaseProbe struct {
	*contextTrackingCacheDriver
	deny     error
	acquired int
	renewed  int
	released int
}

func (probe *contextTagLeaseProbe) AcquireLockContext(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	probe.acquired++
	if probe.deny != nil {
		return false, probe.deny
	}
	return probe.contextTrackingCacheDriver.AcquireLockContext(ctx, key, owner, ttl)
}

func (probe *contextTagLeaseProbe) RenewLockContext(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	probe.renewed++
	return probe.contextTrackingCacheDriver.RenewLockContext(ctx, key, owner, ttl)
}

func (probe *contextTagLeaseProbe) ReleaseLockContext(ctx context.Context, key, owner string) (bool, error) {
	probe.released++
	return probe.contextTrackingCacheDriver.ReleaseLockContext(ctx, key, owner)
}

func TestNamespaceContextOnlyTagLease(t *testing.T) {
	for index, name := range []string{"single", "double", "triple"} {
		for _, version := range []string{"legacy", "current"} {
			t.Run(name+"/"+version, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					assertContextOnlyTagLease(t, index+1, version == "legacy")
				})
			})
		}
	}
}

func assertContextOnlyTagLease(t *testing.T, depth int, legacy bool) {
	t.Helper()
	memory := cacheDriver.NewMemory()
	probe := &contextTagLeaseProbe{
		contextTrackingCacheDriver: &contextTrackingCacheDriver{Memory: memory},
		deny:                       errors.New("后端拒绝标签租约"),
	}
	// 叶子只公开上下文锁方法；同步锁和续期接口不会通过接口组合泄漏出来。
	var driver Driver = struct {
		Driver
		ContextualDistributedLocker
	}{memory, probe}
	for range depth {
		namespace, err := NewNamespaceDriver(driver, "tenant:", "")
		if err != nil {
			t.Fatal(err)
		}
		driver = namespace
	}
	if legacy {
		driver = legacyTagLeaseNamespace{driver.(*NamespaceDriver)}
	}
	manager := NewCache(nil, driver)
	tagged, err := manager.Tag("accounts")
	if err != nil {
		t.Fatal(err)
	}
	err = tagged.Set("account", "value")
	if depth == 1 {
		if err != nil || probe.acquired != 0 {
			t.Fatalf("单层上下文锁后端原有的无租约写入发生变化：%v %d", err, probe.acquired)
		}
		if value, found, err := driver.Get("account"); err != nil || !found || value != "value" {
			t.Fatalf("单层原有写入未保存业务值：%v %t %v", value, found, err)
		}
		return
	}
	if !errors.Is(err, probe.deny) || probe.acquired != 1 {
		t.Errorf("上下文租约拒绝未阻止标签写入：%v，获取次数 %d", err, probe.acquired)
	}
	if _, found, err := driver.Get("account"); found || err != nil {
		t.Errorf("租约拒绝后留下了业务值：%t %v", found, err)
	}
	probe.deny = nil
	err = manager.withTagMutationLockContext(t.Context(), driver, func(ctx context.Context) error {
		if _, held := ctx.Value(tagMutationLeaseKey{}).(tagMutationLease); !held {
			t.Error("回调未获得上下文租约")
		}
		synctest.Wait()
		time.Sleep(tagMutationLockTTL / 3)
		synctest.Wait()
		return nil
	})
	if err != nil || probe.acquired != 2 || probe.renewed != 1 || probe.released != 1 {
		t.Errorf("上下文租约未完整获取、续期与释放：%v %d/%d/%d", err, probe.acquired, probe.renewed, probe.released)
	}
}
