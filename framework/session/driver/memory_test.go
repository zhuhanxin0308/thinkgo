package driver

import (
	"errors"
	"strconv"
	"sync"
	"testing"
)

// TestMemoryZeroValueAndFoundSemantics 验证零值驱动可直接使用且能区分缺失与空字符串。
func TestMemoryZeroValueAndFoundSemantics(t *testing.T) {
	var driver Memory
	if value, found, err := driver.Read("missing"); err != nil || found || value != "" {
		t.Fatalf("零值 Memory 缺失语义错误: value=%q found=%t err=%v", value, found, err)
	}
	if err := driver.Write("empty", ""); err != nil {
		t.Fatalf("零值 Memory 写入失败: %v", err)
	}
	if value, found, err := driver.Read("empty"); err != nil || !found || value != "" {
		t.Fatalf("Memory 空值语义错误: value=%q found=%t err=%v", value, found, err)
	}
	if err := driver.Delete("missing"); err != nil {
		t.Fatalf("删除缺失 Memory Session 应幂等: %v", err)
	}
	if err := driver.Clear(); err != nil {
		t.Fatalf("清空 Memory Session 失败: %v", err)
	}
	if _, found, _ := driver.Read("empty"); found {
		t.Fatal("Clear 后 Memory Session 仍存在")
	}
	if err := driver.Write("bad/id", "value"); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("Memory 应拒绝非法 ID，实际为 %v", err)
	}
	if err := driver.Update("valid", nil); !errors.Is(err, ErrInvalidSessionUpdate) {
		t.Fatalf("Memory 应拒绝 nil 更新回调，实际为 %v", err)
	}
}

// TestMemoryUpdateIsAtomicAndRollsBackCallbackErrors 验证原子更新不丢失并发写且回调失败不污染状态。
func TestMemoryUpdateIsAtomicAndRollsBackCallbackErrors(t *testing.T) {
	driver := NewMemory()
	const workers = 100
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := driver.Update("counter", func(current string, found bool) (string, bool, error) {
				value := 0
				if found {
					value, _ = strconv.Atoi(current)
				}
				return strconv.Itoa(value + 1), false, nil
			}); err != nil {
				t.Errorf("Memory 原子更新失败: %v", err)
			}
		}()
	}
	wg.Wait()
	value, found, err := driver.Read("counter")
	if err != nil || !found || value != strconv.Itoa(workers) {
		t.Fatalf("Memory 原子更新结果错误: value=%q found=%t err=%v", value, found, err)
	}

	callbackErr := errors.New("abort")
	if err = driver.Update("counter", func(string, bool) (string, bool, error) {
		return "corrupt", false, callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("回调错误应原样返回，实际为 %v", err)
	}
	if value, _, _ = driver.Read("counter"); value != strconv.Itoa(workers) {
		t.Fatalf("失败回调不应污染原值，实际为 %q", value)
	}
	if err = driver.Update("counter", func(string, bool) (string, bool, error) {
		return "", true, nil
	}); err != nil {
		t.Fatalf("原子删除失败: %v", err)
	}
	if _, found, _ = driver.Read("counter"); found {
		t.Fatal("原子删除后键仍然存在")
	}
}
