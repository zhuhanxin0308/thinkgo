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

func distinctShardSessionIDs(t *testing.T, driver *Memory, count int) []string {
	t.Helper()
	used := make(map[*sync.Mutex]bool, count)
	ids := make([]string, 0, count)
	for candidate := 1; len(ids) < count && candidate < 10000; candidate++ {
		id := "000000000000000000000000" + strconv.FormatInt(int64(10000000+candidate), 10)
		shard := driver.lockForID(id)
		if used[shard] {
			continue
		}
		used[shard] = true
		ids = append(ids, id)
	}
	if len(ids) != count {
		t.Fatalf("未能找到 %d 个不同分片的 Session ID", count)
	}
	return ids
}

// TestMemoryUpdateCrossShardEvictionDoesNotResurrectStaleSession 验证 Update 锁外回调窗口内
// 发生跨分片容量淘汰或清理时，不会用旧快照复活已淘汰会话并驱逐新会话，且优先保留正在更新的槽位。
func TestMemoryUpdateCrossShardEvictionDoesNotResurrectStaleSession(t *testing.T) {
	t.Run("maxEntries=1_eviction_reverified", func(t *testing.T) {
		mem, err := NewMemoryWithMaxEntries(1)
		if err != nil {
			t.Fatal(err)
		}
		ids := distinctShardSessionIDs(t, mem, 2)
		id1, id2 := ids[0], ids[1]
		alice := `{"version":1,"data":{"user":"alice"}}`
		bob := `{"version":1,"data":{"user":"bob"}}`
		if err := mem.Write(id1, alice); err != nil {
			t.Fatal(err)
		}

		entered := make(chan struct{})
		release := make(chan struct{})
		var enterOnce sync.Once
		var calls int
		updateDone := make(chan error, 1)
		go func() {
			updateDone <- mem.Update(id1, func(current string, found bool) (string, bool, error) {
				calls++
				enterOnce.Do(func() {
					close(entered)
					<-release
				})
				if !found {
					return "", true, nil
				}
				return `{"version":1,"data":{"user":"alice-updated"}}`, false, nil
			})
		}()

		<-entered
		if err := mem.Write(id2, bob); err != nil {
			close(release)
			t.Fatalf("写入新会话失败: %v", err)
		}
		if _, found1, _ := mem.Read(id1); found1 {
			close(release)
			t.Fatal("容量上限为 1 时写入新会话后旧会话应已被淘汰")
		}
		if got2, found2, _ := mem.Read(id2); !found2 || got2 != bob {
			close(release)
			t.Fatalf("新会话未成功保存: found=%t got=%q", found2, got2)
		}

		close(release)
		if err := <-updateDone; err != nil {
			t.Fatalf("Update 返回错误: %v", err)
		}
		if calls != 2 {
			t.Fatalf("被淘汰条目应在提交锁内按最新缺失状态重跑一次回调: calls=%d", calls)
		}
		if _, found1, _ := mem.Read(id1); found1 {
			t.Fatal("已淘汰旧会话被 Update 错误复活")
		}
		if got2, found2, _ := mem.Read(id2); !found2 || got2 != bob {
			t.Fatalf("活跃新会话被在途旧 Update 误淘汰: found=%t got=%q", found2, got2)
		}
	})

	t.Run("maxEntries=2_skips_active_update_slot", func(t *testing.T) {
		mem, err := NewMemoryWithMaxEntries(2)
		if err != nil {
			t.Fatal(err)
		}
		ids := distinctShardSessionIDs(t, mem, 3)
		id1, id2, id3 := ids[0], ids[1], ids[2]
		if err := mem.Write(id1, `{"version":1,"data":{"n":1}}`); err != nil {
			t.Fatal(err)
		}
		if err := mem.Write(id2, `{"version":1,"data":{"n":2}}`); err != nil {
			t.Fatal(err)
		}

		entered := make(chan struct{})
		release := make(chan struct{})
		updateDone := make(chan error, 1)
		go func() {
			updateDone <- mem.Update(id1, func(current string, found bool) (string, bool, error) {
				if !found {
					return "", false, errors.New("活跃更新槽位不应被跨分片优先淘汰")
				}
				close(entered)
				<-release
				return `{"version":1,"data":{"n":11}}`, false, nil
			})
		}()

		<-entered
		if err := mem.Write(id3, `{"version":1,"data":{"n":3}}`); err != nil {
			close(release)
			t.Fatal(err)
		}
		close(release)
		if err := <-updateDone; err != nil {
			t.Fatal(err)
		}
		if got1, found1, _ := mem.Read(id1); !found1 || got1 != `{"version":1,"data":{"n":11}}` {
			t.Fatalf("正在 Update 的会话结果错误: found=%t got=%q", found1, got1)
		}
		if _, found2, _ := mem.Read(id2); found2 {
			t.Fatal("空闲旧槽位 id2 应先于在途更新槽位被淘汰")
		}
		if got3, found3, _ := mem.Read(id3); !found3 || got3 != `{"version":1,"data":{"n":3}}` {
			t.Fatalf("新写入槽位 id3 丢失: found=%t got=%q", found3, got3)
		}
	})

	t.Run("clear_generation_fences_in_flight_callback", func(t *testing.T) {
		mem, err := NewMemoryWithMaxEntries(2)
		if err != nil {
			t.Fatal(err)
		}
		const id = "00000000000000000000000000000001"
		if err := mem.Write(id, `{"version":1,"data":{"user":"alice"}}`); err != nil {
			t.Fatal(err)
		}
		err = mem.Update(id, func(string, bool) (string, bool, error) {
			mem.mutex.Lock()
			mem.clearSeq++
			mem.entries = make(map[string]memorySessionEntry)
			mem.mutex.Unlock()
			return `{"version":1,"data":{"user":"stale"}}`, false, nil
		})
		if !errors.Is(err, ErrSessionCleared) {
			t.Fatalf("跨越清理代际的提交应返回 ErrSessionCleared，实际为 %v", err)
		}
		if _, found, _ := mem.Read(id); found {
			t.Fatal("跨越清理代际的提交不应复活旧会话")
		}
	})
}
