package cache

import (
	"context"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func nestedCapabilityDriver(t *testing.T, backend Driver) *NamespaceDriver {
	t.Helper()
	outer, err := NewNamespaceDriver(backend, "shared:", "")
	if err != nil {
		t.Fatal(err)
	}
	nested, err := NewNamespaceDriver(outer, "tenant:", "")
	if err != nil {
		t.Fatal(err)
	}
	return nested
}

func TestNestedNamespaceKeepsTagMutationLease(t *testing.T) {
	driver := nestedCapabilityDriver(t, cacheDriver.NewMemory())
	manager := NewCache(nil, driver)
	err := manager.withTagMutationLockContext(t.Context(), driver, func(ctx context.Context) error {
		lease, ok := ctx.Value(tagMutationLeaseKey{}).(tagMutationLease)
		if !ok {
			t.Fatal("标签写入未持有后端租约")
		}
		if acquired, err := driver.AcquireLock(lease.key, "other", time.Second); err != nil || acquired {
			t.Fatalf("租约未保护同一个嵌套空间：%t %v", acquired, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNestedNamespaceKeepsAtomicInvalidation(t *testing.T) {
	driver := nestedCapabilityDriver(t, cacheDriver.NewMemory())
	manager := NewCache(nil, driver)
	const key = "invoice"
	if err := manager.Set(key, "before", 0); err != nil {
		t.Fatal(err)
	}
	old, found, err := driver.Get(key)
	if err != nil || !found {
		t.Fatalf("读取原记录失败：%t %v", found, err)
	}
	if err := manager.Forget(key); err != nil {
		t.Fatal(err)
	}
	// 在失效之后模拟旧记录迟到落盘，公开读取仍应受原子驱动的水位保护。
	if err := driver.Set(key, old, 0); err != nil {
		t.Fatal(err)
	}
	if value, found, err := manager.Get(key); err != nil || found {
		t.Fatalf("支持原子更新的后端失去失效保护：%v %t %v", value, found, err)
	}
}

func TestNestedNamespaceTagsWithoutDistributedLocks(t *testing.T) {
	// 接口包装只暴露基础缓存操作，模拟没有分布式租约的已有自定义驱动。
	backend := struct{ Driver }{cacheDriver.NewMemory()}
	manager := NewCache(nil, nestedCapabilityDriver(t, backend))
	tagged, err := manager.Tag("accounts")
	if err != nil {
		t.Fatal(err)
	}
	if err := tagged.Set("user", "active", 0); err != nil {
		t.Fatalf("无分布式锁驱动无法保存标签：%v", err)
	}
	if value, found, err := manager.Get("user"); err != nil || !found || value != "active" {
		t.Fatalf("标签值写入失败：%v %t %v", value, found, err)
	}
	if err := tagged.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := manager.Get("user"); err != nil || found {
		t.Fatalf("标签清理失败：%t %v", found, err)
	}
}
