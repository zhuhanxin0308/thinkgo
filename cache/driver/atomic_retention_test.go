package driver

import (
	"errors"
	"os"
	"testing"
	"time"
)

// TestFileAtomicUpdateContract 验证真实文件后端同样遵守回调失败回滚、并发更新与删除的共同契约。
func TestFileAtomicUpdateContract(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	testAtomicUpdateContract(t, driver, 0, func(value interface{}) int { return int(value.(float64)) })
}

// TestAtomicUpdatesPreserveAbsoluteExpiry 验证更新不能续长已有 TTL，未命中的新值保持永久有效语义。
func TestAtomicUpdatesPreserveAbsoluteExpiry(t *testing.T) {
	fileDriver, _ := newTestFileDriver(t)
	for name, driver := range map[string]interface {
		atomicCacheDriver
		UpdatePreserveTTL(string, func(interface{}, bool) (interface{}, bool, error)) error
	}{"file": fileDriver, "memory": NewMemory()} {
		t.Run(name, func(t *testing.T) {
			expiry := func() time.Time {
				if memory, ok := driver.(*Memory); ok {
					memory.lock.RLock()
					defer memory.lock.RUnlock()
					return memory.items["value"].expiry
				}
				item, found, err := fileDriver.readItemLocked("value", time.Now())
				if err != nil || !found {
					t.Fatalf("读取持久过期时间失败: %t %v", found, err)
				}
				return item.Expiry
			}
			if err := driver.Set("value", "old", time.Minute); err != nil {
				t.Fatal(err)
			}
			before := expiry()
			if err := driver.UpdatePreserveTTL("value", func(value interface{}, found bool) (interface{}, bool, error) {
				if !found || value != "old" {
					t.Fatalf("回调未收到原值: %v %t", value, found)
				}
				return "new", false, nil
			}); err != nil {
				t.Fatal(err)
			}
			if !before.Equal(expiry()) {
				t.Fatal("更新改变了绝对过期时间")
			}
			if err := driver.UpdatePreserveTTL("missing", func(_ interface{}, found bool) (interface{}, bool, error) {
				if found {
					t.Fatal("不存在的条目被报告为命中")
				}
				return "created", false, nil
			}); err != nil {
				t.Fatal(err)
			}
			if value, found, err := driver.Get("missing"); err != nil || !found || value != "created" {
				t.Fatalf("未命中更新没有创建值: %v %t %v", value, found, err)
			}
		})
	}
}

// TestFileAtomicInputFailuresPreserveExistingData 验证配置、回调与持久化失败都不破坏原有数据。
func TestFileAtomicInputFailuresPreserveExistingData(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	if err := driver.Set("value", "original", 0); err != nil {
		t.Fatal(err)
	}
	if err := driver.Update("value", -time.Second, nil); !errors.Is(err, ErrInvalidDriverTTL) {
		t.Fatalf("负 TTL 未被拒绝: %v", err)
	}
	if err := driver.UpdatePreserveTTL("value", nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatalf("空回调未被拒绝: %v", err)
	}
	if err := driver.Update("value", 0, func(interface{}, bool) (interface{}, bool, error) {
		return make(chan int), false, nil
	}); err == nil {
		t.Fatal("不可序列化结果必须报告写入失败")
	}
	if value, found, err := driver.Get("value"); err != nil || !found || value != "original" {
		t.Fatalf("失败更新破坏原值: %v %t %v", value, found, err)
	}
	if err := driver.Update("missing", 0, func(interface{}, bool) (interface{}, bool, error) {
		return nil, true, nil
	}); err != nil {
		t.Fatalf("删除不存在条目应幂等: %v", err)
	}
	guard := driver.mutationGuardPath("blocked")
	if err := os.Mkdir(guard, 0o700); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := driver.Update("blocked", 0, func(interface{}, bool) (interface{}, bool, error) {
		called = true
		return "unsafe", false, nil
	}); err == nil || called {
		t.Fatalf("非普通文件的守卫未阻断写入: called=%t err=%v", called, err)
	}
}
