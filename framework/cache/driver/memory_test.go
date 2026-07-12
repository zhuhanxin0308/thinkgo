package driver

import (
	"errors"
	"math"
	"testing"
	"time"
)

// TestMemoryCacheNilExpiryAndZeroValue 验证零值驱动、nil 命中和过期清理语义。
func TestMemoryCacheNilExpiryAndZeroValue(t *testing.T) {
	driver := &Memory{}
	if err := driver.Set("nil", nil, 0); err != nil {
		t.Fatalf("零值内存驱动写入失败: %v", err)
	}
	value, found, err := driver.Get("nil")
	if err != nil || !found || value != nil {
		t.Fatalf("nil 缓存命中错误: value=%#v found=%t err=%v", value, found, err)
	}
	if err = driver.Set("expired", "value", time.Millisecond); err != nil {
		t.Fatalf("写入过期测试值失败: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, found, err = driver.Get("expired"); err != nil || found {
		t.Fatalf("过期缓存应未命中: found=%t err=%v", found, err)
	}
	if _, exists := driver.items["expired"]; exists {
		t.Fatal("读取过期缓存后应清理内存项")
	}
}

// TestMemoryCounterIsStrictAndPreservesTTL 验证内存计数拒绝截断与溢出，并继承原过期时间。
func TestMemoryCounterIsStrictAndPreservesTTL(t *testing.T) {
	driver := NewMemory()
	if err := driver.Set("counter", float64(5), 50*time.Millisecond); err != nil {
		t.Fatalf("写入计数失败: %v", err)
	}
	if value, err := driver.Inc("counter", 2); err != nil || value != 7 {
		t.Fatalf("合法整数浮点计数失败: value=%d err=%v", value, err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, found, err := driver.Get("counter"); err != nil || found {
		t.Fatalf("计数操作不应移除原 TTL: found=%t err=%v", found, err)
	}

	if err := driver.Set("fraction", 1.5, 0); err != nil {
		t.Fatalf("写入小数失败: %v", err)
	}
	if _, err := driver.Inc("fraction", 1); !errors.Is(err, ErrInvalidCounterValue) {
		t.Fatalf("小数计数值应被拒绝，实际为 %v", err)
	}
	if err := driver.Set("overflow", int64(math.MinInt64), 0); err != nil {
		t.Fatalf("写入溢出边界失败: %v", err)
	}
	if _, err := driver.Dec("overflow", 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("递减溢出应返回 ErrCounterOverflow，实际为 %v", err)
	}
	if _, err := driver.Inc("negative-step", -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负递增步长应返回 ErrInvalidCounterStep，实际为 %v", err)
	}
	if _, err := driver.Dec("negative-step", -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负递减步长应返回 ErrInvalidCounterStep，实际为 %v", err)
	}
}

// TestMemoryClearDoesNotReleaseLocks 验证缓存数据清空与锁生命周期相互独立。
func TestMemoryClearDoesNotReleaseLocks(t *testing.T) {
	driver := NewMemory()
	if acquired, err := driver.AcquireLock("job", "owner-a", time.Minute); err != nil || !acquired {
		t.Fatalf("获取内存锁失败: acquired=%t err=%v", acquired, err)
	}
	if err := driver.Set("key", "value", 0); err != nil {
		t.Fatalf("写入缓存失败: %v", err)
	}
	if err := driver.Clear(); err != nil {
		t.Fatalf("清空内存缓存失败: %v", err)
	}
	if acquired, err := driver.AcquireLock("job", "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("Clear 不得释放内存锁: acquired=%t err=%v", acquired, err)
	}
	if released, err := driver.ReleaseLock("job", "owner-a"); err != nil || !released {
		t.Fatalf("释放内存锁失败: released=%t err=%v", released, err)
	}
}

// TestMemoryCRUDAndLockOwnership 验证存在判断、删除、缺失计数及锁 owner/过期边界。
func TestMemoryCRUDAndLockOwnership(t *testing.T) {
	driver := NewMemory()
	if err := driver.Set("key", "value", 0); err != nil {
		t.Fatalf("写入内存缓存失败: %v", err)
	}
	if exists, err := driver.Has("key"); err != nil || !exists {
		t.Fatalf("Has 未识别已存在键: exists=%t err=%v", exists, err)
	}
	if err := driver.Delete("key"); err != nil {
		t.Fatalf("删除内存缓存失败: %v", err)
	}
	if exists, err := driver.Has("key"); err != nil || exists {
		t.Fatalf("删除后 Has 仍命中: exists=%t err=%v", exists, err)
	}
	if value, err := driver.Dec("missing-counter", 3); err != nil || value != -3 {
		t.Fatalf("缺失计数递减应从 0 开始: value=%d err=%v", value, err)
	}
	if acquired, err := driver.AcquireLock("short", "owner-a", 10*time.Millisecond); err != nil || !acquired {
		t.Fatalf("获取短锁失败: acquired=%t err=%v", acquired, err)
	}
	if released, err := driver.ReleaseLock("short", "owner-b"); err != nil || released {
		t.Fatalf("非 owner 不得释放锁: released=%t err=%v", released, err)
	}
	time.Sleep(20 * time.Millisecond)
	if released, err := driver.ReleaseLock("short", "owner-a"); err != nil || released {
		t.Fatalf("过期 owner 不应报告释放成功: released=%t err=%v", released, err)
	}
	if acquired, err := driver.AcquireLock("short", "owner-b", time.Second); err != nil || !acquired {
		t.Fatalf("过期后新 owner 应获取成功: acquired=%t err=%v", acquired, err)
	}
}
