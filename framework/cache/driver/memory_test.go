package driver

import (
	"errors"
	"math"
	"testing"
	"time"
)

// TestMemoryCacheCopiesMutableValues 验证 Set 和 Get 都不会把可变对象的内部引用暴露给调用方。
func TestMemoryCacheCopiesMutableValues(t *testing.T) {
	driver := NewMemory()
	input := map[string]interface{}{
		"name":  "origin",
		"items": []string{"first"},
	}
	if err := driver.Set("mutable", input, 0); err != nil {
		t.Fatalf("写入可变缓存失败: %v", err)
	}
	input["name"] = "changed-before-get"
	input["items"].([]string)[0] = "changed-before-get"

	value, found, err := driver.Get("mutable")
	if err != nil || !found {
		t.Fatalf("读取可变缓存失败: value=%#v found=%t err=%v", value, found, err)
	}
	got := value.(map[string]interface{})
	if got["name"] != "origin" || got["items"].([]string)[0] != "first" {
		t.Fatalf("Set 不应保存调用方后续修改: %#v", got)
	}
	got["name"] = "changed-after-get"
	got["items"].([]string)[0] = "changed-after-get"

	value, found, err = driver.Get("mutable")
	if err != nil || !found {
		t.Fatalf("再次读取可变缓存失败: value=%#v found=%t err=%v", value, found, err)
	}
	got = value.(map[string]interface{})
	if got["name"] != "origin" || got["items"].([]string)[0] != "first" {
		t.Fatalf("Get 不应暴露缓存内部引用: %#v", got)
	}
}

type memoryCloneNode struct {
	Name string
	Next *memoryCloneNode
}

// TestMemoryCacheCopiesNestedAndCyclicValues 验证深层 slice、指针、数组和循环 map 的复制边界。
func TestMemoryCacheCopiesNestedAndCyclicValues(t *testing.T) {
	driver := NewMemory()
	node := &memoryCloneNode{Name: "origin"}
	node.Next = node
	cyclic := map[string]interface{}{}
	cyclic["self"] = cyclic
	input := map[string]interface{}{
		"node":   node,
		"array":  [2][]int{{1}, {2}},
		"bytes":  []byte("origin"),
		"cyclic": cyclic,
	}
	if err := driver.Set("nested", input, 0); err != nil {
		t.Fatalf("写入嵌套可变缓存失败: %v", err)
	}
	value, found, err := driver.Get("nested")
	if err != nil || !found {
		t.Fatalf("读取嵌套可变缓存失败: value=%#v found=%t err=%v", value, found, err)
	}
	got := value.(map[string]interface{})
	gotNode := got["node"].(*memoryCloneNode)
	if gotNode == node || gotNode.Next != gotNode {
		t.Fatalf("指针和循环引用未被正确复制: got=%#v original=%#v", gotNode, node)
	}
	gotArray := got["array"].([2][]int)
	gotArray[0][0] = 99
	got["bytes"].([]byte)[0] = 'x'
	gotNode.Name = "changed"

	value, found, err = driver.Get("nested")
	if err != nil || !found {
		t.Fatalf("再次读取嵌套缓存失败: value=%#v found=%t err=%v", value, found, err)
	}
	got = value.(map[string]interface{})
	if got["node"].(*memoryCloneNode).Name != "origin" || got["array"].([2][]int)[0][0] != 1 || string(got["bytes"].([]byte)) != "origin" {
		t.Fatalf("嵌套可变值仍然暴露内部引用: %#v", got)
	}
}

// TestMemoryCacheLockRenewal 验证内存驱动续租只接受当前 owner。
func TestMemoryCacheLockRenewal(t *testing.T) {
	driver := NewMemory()
	if acquired, err := driver.AcquireLock("lease", "owner-a", time.Second); err != nil || !acquired {
		t.Fatalf("获取内存租约锁失败: acquired=%t err=%v", acquired, err)
	}
	if renewed, err := driver.RenewLock("lease", "owner-a", time.Minute); err != nil || !renewed {
		t.Fatalf("续租内存锁失败: renewed=%t err=%v", renewed, err)
	}
	if renewed, err := driver.RenewLock("lease", "owner-b", time.Minute); err != nil || renewed {
		t.Fatalf("错误 owner 不得续租内存锁: renewed=%t err=%v", renewed, err)
	}
}

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

// TestMemoryCacheCapacityEvictsOldestEntry 验证有界内存缓存按 FIFO 淘汰最早写入项。
func TestMemoryCacheCapacityEvictsOldestEntry(t *testing.T) {
	driver, err := NewMemoryWithMaxEntries(2)
	if err != nil {
		t.Fatalf("创建有界内存缓存失败: %v", err)
	}
	for _, entry := range []struct{ key, value string }{
		{key: "first", value: "1"},
		{key: "second", value: "2"},
		{key: "third", value: "3"},
	} {
		if err := driver.Set(entry.key, entry.value, 0); err != nil {
			t.Fatalf("写入有界内存缓存失败: key=%s err=%v", entry.key, err)
		}
	}
	if _, found, err := driver.Get("first"); err != nil || found {
		t.Fatalf("超过容量后最早条目应被淘汰: found=%t err=%v", found, err)
	}
	for _, key := range []string{"second", "third"} {
		if value, found, err := driver.Get(key); err != nil || !found || value == nil {
			t.Fatalf("有界内存缓存应保留较新条目: key=%s value=%#v found=%t err=%v", key, value, found, err)
		}
	}
}

// TestMemoryMetadataDoesNotEvictBusinessEntries 验证有界缓存写入内部元数据时不会淘汰业务项。
func TestMemoryMetadataDoesNotEvictBusinessEntries(t *testing.T) {
	driver, err := NewMemoryWithMaxEntries(2)
	if err != nil {
		t.Fatalf("创建有界内存缓存失败: %v", err)
	}
	if err = driver.Set("business", "value", 0); err != nil {
		t.Fatalf("写入业务项失败: %v", err)
	}
	if err = driver.Set("__thinkgo_tag__:metadata", []string{"business"}, 0); err != nil {
		t.Fatalf("写入内部元数据失败: %v", err)
	}
	if err = driver.Set("__thinkgo_tag__:metadata-2", []string{"business"}, 0); !errors.Is(err, ErrMemoryCapacityExhausted) {
		t.Fatalf("容量不足时应拒绝新增元数据，实际错误为 %v", err)
	}
	if value, found, getErr := driver.Get("business"); getErr != nil || !found || value != "value" {
		t.Fatalf("元数据容量不足不得淘汰业务项: value=%#v found=%t err=%v", value, found, getErr)
	}
}

// TestMemoryBusinessWritePreservesMetadata 验证业务项淘汰只选择普通项，不会破坏已写入的元数据。
func TestMemoryBusinessWritePreservesMetadata(t *testing.T) {
	driver, err := NewMemoryWithMaxEntries(2)
	if err != nil {
		t.Fatalf("创建有界内存缓存失败: %v", err)
	}
	if err = driver.Set("__thinkgo_tag__:metadata", []string{"business"}, 0); err != nil {
		t.Fatalf("写入内部元数据失败: %v", err)
	}
	if err = driver.Set("business-1", "one", 0); err != nil {
		t.Fatalf("写入第一个业务项失败: %v", err)
	}
	if err = driver.Set("business-2", "two", 0); err != nil {
		t.Fatalf("写入第二个业务项失败: %v", err)
	}
	if _, found, getErr := driver.Get("__thinkgo_tag__:metadata"); getErr != nil || !found {
		t.Fatalf("普通项淘汰不得删除内部元数据: found=%t err=%v", found, getErr)
	}
}

// TestNewMemoryWithMaxEntriesRejectsNegativeCapacity 验证容量配置不会接受负数。
func TestNewMemoryWithMaxEntriesRejectsNegativeCapacity(t *testing.T) {
	if _, err := NewMemoryWithMaxEntries(-1); !errors.Is(err, ErrInvalidMemoryCapacity) {
		t.Fatalf("负容量应返回 ErrInvalidMemoryCapacity，实际为 %v", err)
	}
	if driver := NewMemory(); driver == nil {
		t.Fatal("兼容构造函数 NewMemory 不应返回 nil")
	}
}
