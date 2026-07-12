package session

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type pausingSessionDriver struct {
	*countingDriver
	pause   bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *pausingSessionDriver) Update(id string, update func(string, bool) (string, bool, error)) error {
	if d.pause {
		d.once.Do(func() { close(d.started) })
		<-d.release
	}
	return d.countingDriver.Update(id, update)
}

// TestConcurrentSetGetDelete 验证请求内的并发读写不会发生数据竞争或 map 崩溃。
func TestConcurrentSetGetDelete(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{
		"name": "SID", "max_data_bytes": 512 << 10,
	}, nil)
	sess, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}

	const goroutines = 20
	const operations = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for index := 0; index < goroutines; index++ {
		go func(worker int) {
			defer wg.Done()
			for operation := 0; operation < operations; operation++ {
				key := fmt.Sprintf("key_%d_%d", worker, operation)
				if setErr := sess.Set(key, operation); setErr != nil {
					t.Errorf("并发 Set 失败: %v", setErr)
					return
				}
				sess.Get(key)
				sess.Has(key)
				if operation%3 == 0 {
					if deleteErr := sess.Delete(key); deleteErr != nil {
						t.Errorf("并发 Delete 失败: %v", deleteErr)
						return
					}
				}
			}
		}(index)
	}
	wg.Wait()
}

// TestConcurrentRequestsMergeIndependentMutations 验证同一 ID 的并发请求不会因整包覆盖丢失独立字段。
func TestConcurrentRequestsMergeIndependentMutations(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	cookieValue, _ := persistedSessionFixture(t, manager)

	first, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化第一个并发 Session 失败: %v", err)
	}
	second, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化第二个并发 Session 失败: %v", err)
	}
	if err = first.Set("first", "A"); err != nil {
		t.Fatalf("设置第一个变更失败: %v", err)
	}
	if err = second.Set("second", "B"); err != nil {
		t.Fatalf("设置第二个变更失败: %v", err)
	}

	var wg sync.WaitGroup
	errorsChannel := make(chan error, 2)
	for _, current := range []*Session{first, second} {
		wg.Add(1)
		go func(sess *Session) {
			defer wg.Done()
			errorsChannel <- sess.Save()
		}(current)
	}
	wg.Wait()
	close(errorsChannel)
	for saveErr := range errorsChannel {
		if saveErr != nil {
			t.Fatalf("并发保存失败: %v", saveErr)
		}
	}

	verify, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("读取合并后的 Session 失败: %v", err)
	}
	firstValue, firstFound := verify.Get("first")
	secondValue, secondFound := verify.Get("second")
	if !firstFound || !secondFound || firstValue != "A" || secondValue != "B" {
		t.Fatalf("并发独立变更发生丢失: first=%#v/%t second=%#v/%t", firstValue, firstFound, secondValue, secondFound)
	}
}

// TestClearWinsAgainstCurrentPersistedState 验证 Clear 在原子更新时清除后端最新状态再应用后续变更。
func TestClearWinsAgainstCurrentPersistedState(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	cookieValue, _ := persistedSessionFixture(t, manager)
	clearer, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化清理请求失败: %v", err)
	}
	concurrent, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化并发写请求失败: %v", err)
	}
	if err = concurrent.Set("concurrent", true); err != nil {
		t.Fatalf("设置并发字段失败: %v", err)
	}
	if err = concurrent.Save(); err != nil {
		t.Fatalf("保存并发字段失败: %v", err)
	}
	if err = clearer.Clear(); err != nil {
		t.Fatalf("清空 Session 失败: %v", err)
	}
	if err = clearer.Set("after_clear", "kept"); err != nil {
		t.Fatalf("设置清空后字段失败: %v", err)
	}
	if err = clearer.Save(); err != nil {
		t.Fatalf("保存清空操作失败: %v", err)
	}
	verify, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("读取清空后的 Session 失败: %v", err)
	}
	if verify.Has("uid") || verify.Has("concurrent") || !verify.Has("after_clear") {
		t.Fatalf("Clear 原子语义错误: uid=%t concurrent=%t after=%t", verify.Has("uid"), verify.Has("concurrent"), verify.Has("after_clear"))
	}
}

// TestConcurrentSaveAndSetRetainsLatestDirtyState 验证 Save 不持有数据锁执行 I/O，且并发修改不会被错误清除。
func TestConcurrentSaveAndSetRetainsLatestDirtyState(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	recorder := httptest.NewRecorder()
	sess, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}
	if err = sess.Set("initial", true); err != nil {
		t.Fatalf("设置初始字段失败: %v", err)
	}

	var wg sync.WaitGroup
	for index := 0; index < 10; index++ {
		wg.Add(2)
		go func(value int) {
			defer wg.Done()
			if setErr := sess.Set(fmt.Sprintf("k_%d", value), value); setErr != nil {
				t.Errorf("并发设置失败: %v", setErr)
			}
		}(index)
		go func() {
			defer wg.Done()
			if saveErr := sess.Save(); saveErr != nil {
				t.Errorf("并发保存失败: %v", saveErr)
			}
		}()
	}
	wg.Wait()
	if err = sess.Save(); err != nil {
		t.Fatalf("最终保存失败: %v", err)
	}
	for index := 0; index < 10; index++ {
		if !sess.Has(fmt.Sprintf("k_%d", index)) {
			t.Fatalf("并发字段 k_%d 丢失", index)
		}
	}
}

// TestDestroyRejectsMutationsWhileRevocationIsInFlight 验证撤销期间的同请求写入不会先成功再被静默丢弃。
func TestDestroyRejectsMutationsWhileRevocationIsInFlight(t *testing.T) {
	driver := &pausingSessionDriver{
		countingDriver: newCountingDriver(),
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	cookieValue, _ := persistedSessionFixture(t, manager)
	requestSession, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化销毁请求失败: %v", err)
	}
	driver.pause = true
	destroyed := make(chan error, 1)
	go func() { destroyed <- requestSession.Destroy() }()
	select {
	case <-driver.started:
	case <-time.After(time.Second):
		t.Fatal("Destroy 未进入驱动撤销阶段")
	}
	if err = requestSession.Set("late", true); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("撤销期间写入应返回 ErrSessionBusy，实际为 %v", err)
	}
	close(driver.release)
	if err = <-destroyed; err != nil {
		t.Fatalf("完成撤销失败: %v", err)
	}
	if requestSession.Has("late") {
		t.Fatal("撤销期间被拒绝的写入不应进入本地状态")
	}
}
