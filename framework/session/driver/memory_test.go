package driver

import (
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
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

// TestMemorySessionUpdatesDifferentIDsDoNotShareOneGlobalCallbackLock 验证不同 Session ID 的回调不会互相阻塞。
func TestMemorySessionUpdatesDifferentIDsDoNotShareOneGlobalCallbackLock(t *testing.T) {
	driver := NewMemory()
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- driver.Update("first", func(current string, found bool) (string, bool, error) {
			close(started)
			<-release
			return current + "a", false, nil
		})
	}()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- driver.Update("second", func(string, bool) (string, bool, error) {
			return "b", false, nil
		})
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("不同 Session ID 的更新失败: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("不同 Session ID 的更新被另一个回调阻塞")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("第一个 Session 更新失败: %v", err)
	}
}

// TestMemorySessionCapacityAndGC 验证容量淘汰、过期信封回收和损坏记录清理。
func TestMemorySessionCapacityAndGC(t *testing.T) {
	driver, err := NewMemoryWithMaxEntries(2)
	if err != nil {
		t.Fatalf("创建有界 Session 驱动失败: %v", err)
	}
	if err = driver.Write("first", `{"version":1,"data":{}}`); err != nil {
		t.Fatalf("写入第一条 Session 失败: %v", err)
	}
	if err = driver.Write("second", `{"version":1,"data":{}}`); err != nil {
		t.Fatalf("写入第二条 Session 失败: %v", err)
	}
	if err = driver.Write("third", `{"version":1,"data":{}}`); err != nil {
		t.Fatalf("有界 Session 应淘汰旧记录: %v", err)
	}
	if _, found, readErr := driver.Read("first"); readErr != nil || found {
		t.Fatalf("容量超限后第一条记录应被淘汰: found=%t err=%v", found, readErr)
	}

	expired := struct {
		Version  int                    `json:"version"`
		ExpireAt int64                  `json:"expire_at"`
		Data     map[string]interface{} `json:"data"`
	}{Version: 1, ExpireAt: time.Now().Add(-time.Minute).Unix(), Data: map[string]interface{}{}}
	expiredBytes, _ := json.Marshal(expired)
	if err = driver.Write("expired", string(expiredBytes)); err != nil {
		t.Fatalf("写入过期 Session 失败: %v", err)
	}
	if removed, gcErr := driver.GC(time.Hour); gcErr != nil || removed < 1 {
		t.Fatalf("Session GC 应清理过期记录: removed=%d err=%v", removed, gcErr)
	}
	if _, found, readErr := driver.Read("expired"); readErr != nil || found {
		t.Fatalf("GC 后过期记录不应存在: found=%t err=%v", found, readErr)
	}
}

func TestMemorySessionCapacityUsesFIFOOrder(t *testing.T) {
	driver, err := NewMemoryWithMaxEntries(2)
	if err != nil {
		t.Fatalf("创建有界 Session 驱动失败: %v", err)
	}
	for _, id := range []string{"first", "second"} {
		if err := driver.Write(id, `{"version":1,"data":{}}`); err != nil {
			t.Fatalf("写入 Session %q 失败: %v", id, err)
		}
	}
	if err := driver.Write("first", `{"version":1,"data":{"updated":true}}`); err != nil {
		t.Fatalf("更新 Session 失败: %v", err)
	}
	if err := driver.Write("third", `{"version":1,"data":{}}`); err != nil {
		t.Fatalf("写入触发淘汰的 Session 失败: %v", err)
	}
	if _, found, readErr := driver.Read("first"); readErr != nil || found {
		t.Fatalf("FIFO 应淘汰最早写入的 first，found=%t err=%v", found, readErr)
	}
	if _, found, readErr := driver.Read("second"); readErr != nil || !found {
		t.Fatalf("更新不应改变 second 的保留状态，found=%t err=%v", found, readErr)
	}
}
