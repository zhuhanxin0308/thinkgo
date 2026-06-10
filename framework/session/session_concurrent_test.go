package session

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"thinkgo/framework/cookie"
)

// TestConcurrentSetGet 验证并发调用 Set/Get/Has/Delete 不会触发 map 并发写 panic。
// 使用 -race 标志运行可检测数据竞争：go test -race -run TestConcurrentSetGet
func TestConcurrentSetGet(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "SESS",
		"expire": 600,
	}, map[string]interface{}{})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	recorder := httptest.NewRecorder()
	sess := manager.NewRequestSession(req, recorder)

	const goroutines = 50
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// 并发写入不同 key
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				key := fmt.Sprintf("key_%d_%d", idx, j)
				sess.Set(key, j)
				sess.Get(key)
				sess.Has(key)
			}
		}(i)
	}

	wg.Wait()
}

// TestConcurrentSetAndDelete 验证并发 Set 和 Delete 不会 panic。
func TestConcurrentSetAndDelete(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "SESS",
		"expire": 600,
	}, map[string]interface{}{})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	recorder := httptest.NewRecorder()
	sess := manager.NewRequestSession(req, recorder)

	const goroutines = 30

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// 一半 goroutine 写入
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("shared_%d", j%10)
				sess.Set(key, idx)
			}
		}(i)
	}

	// 一半 goroutine 删除
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("shared_%d", j%10)
				sess.Delete(key)
			}
		}()
	}

	wg.Wait()
}

// TestConcurrentClearAndSet 验证并发 Clear 和 Set 不会 panic。
func TestConcurrentClearAndSet(t *testing.T) {
	driver := &countingDriver{}
	manager := newTestSessionManager(driver, map[string]interface{}{
		"name":   "SESS",
		"expire": 600,
	}, map[string]interface{}{})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	recorder := httptest.NewRecorder()
	sess := manager.NewRequestSession(req, recorder)

	const goroutines = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// 一半 goroutine 写入
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				sess.Set(fmt.Sprintf("key_%d", j), idx)
			}
		}(i)
	}

	// 一半 goroutine 清空
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				sess.Clear()
			}
		}()
	}

	wg.Wait()
}

// TestConcurrentSaveAndSet 验证并发 Save 和 Set 不会 panic（跨锁操作场景）。
func TestConcurrentSaveAndSet(t *testing.T) {
	driver := &countingDriver{}
	manager := NewSession(map[string]interface{}{
		"name":   "SESS",
		"expire": 600,
	}, driver, cookie.NewCookie(map[string]interface{}{}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	recorder := httptest.NewRecorder()
	sess := manager.NewRequestSession(req, recorder)
	sess.SetResponseWriter(recorder)

	const goroutines = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// 一半 goroutine 写入
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				sess.Set(fmt.Sprintf("k_%d", j), idx)
			}
		}(i)
	}

	// 一半 goroutine 保存
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := sess.Save(); err != nil {
					t.Errorf("并发保存 Session 不应报错: %v", err)
				}
			}
		}()
	}

	wg.Wait()
}
