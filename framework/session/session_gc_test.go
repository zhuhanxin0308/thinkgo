package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"thinkgo/framework/cookie"
)

type blockingGCDriver struct {
	*countingDriver
	started chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (d *blockingGCDriver) GC(time.Duration) (int, error) {
	d.once.Do(func() { close(d.started) })
	<-d.release
	return 3, d.err
}

// TestGarbageCollectorStopWaitsForActiveRun 验证 stop 返回时后台 GC 已完全退出。
func TestGarbageCollectorStopWaitsForActiveRun(t *testing.T) {
	driver := &blockingGCDriver{
		countingDriver: newCountingDriver(),
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "expire": 60}, nil)
	stop := manager.StartGarbageCollector(time.Millisecond)
	select {
	case <-driver.started:
	case <-time.After(time.Second):
		t.Fatal("后台 GC 未按预期启动")
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("GC 尚未释放时 stop 不得提前返回")
	case <-time.After(20 * time.Millisecond):
	}
	close(driver.release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("释放 GC 后 stop 未及时返回")
	}
	// stop 必须幂等，重复调用不能阻塞或关闭已关闭通道。
	stop()
}

// TestGarbageCollectorReportsErrorsAndUnsupportedDrivers 验证直接 GC、日志和不支持驱动的空操作语义。
func TestGarbageCollectorReportsErrorsAndUnsupportedDrivers(t *testing.T) {
	backendErr := errors.New("gc failed")
	driver := &blockingGCDriver{
		countingDriver: newCountingDriver(),
		started:        make(chan struct{}),
		release:        make(chan struct{}),
		err:            backendErr,
	}
	close(driver.release)
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "expire": 0}, nil)
	if removed, err := manager.GC(); removed != 3 || !errors.Is(err, backendErr) {
		t.Fatalf("直接 GC 应传播结果: removed=%d err=%v", removed, err)
	}
	logger := &sessionTestLogger{}
	manager.SetLogger(logger)
	stop := manager.StartGarbageCollector(time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for logger.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	if logger.count() == 0 {
		t.Fatal("后台 GC 错误应写入日志")
	}

	cookieFactory, err := cookie.NewCookie(nil)
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	unsupported, err := NewSession(map[string]interface{}{"name": "SID"}, newCountingDriver(), cookieFactory)
	if err != nil {
		t.Fatalf("创建不支持 GC 的 Session 失败: %v", err)
	}
	if removed, gcErr := unsupported.GC(); gcErr != nil || removed != 0 {
		t.Fatalf("不支持 GC 的驱动应返回零值: removed=%d err=%v", removed, gcErr)
	}
	unsupported.StartGarbageCollector(time.Millisecond)()

	requestSession, err := unsupported.NewRequestSession(
		httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder(),
	)
	if err != nil || requestSession == nil {
		t.Fatalf("GC 测试后 Session 应仍可使用: session=%#v err=%v", requestSession, err)
	}
}
