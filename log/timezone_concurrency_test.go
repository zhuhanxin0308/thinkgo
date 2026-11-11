package log

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type delayedTimezoneDriver struct {
	block   *time.Location
	entered chan struct{}
	release chan struct{}
	blocked atomic.Bool
	mu      sync.Mutex
	current *time.Location
}

func (*delayedTimezoneDriver) SaveEntries([]*LogEntry) error { return nil }
func (*delayedTimezoneDriver) WriteEntry(*LogEntry) error    { return nil }
func (*delayedTimezoneDriver) Close() error                  { return nil }

func (driver *delayedTimezoneDriver) SetLocation(location *time.Location) {
	if location == driver.block && driver.blocked.CompareAndSwap(false, true) {
		close(driver.entered)
		<-driver.release
	}
	driver.mu.Lock()
	driver.current = location
	driver.mu.Unlock()
}

// TestLogConcurrentLocationPublication 通过阻塞旧通知，验证新增驱动、更新与注册通道的最终时区一致。
func TestLogConcurrentLocationPublication(t *testing.T) {
	const completionTimeout = 3 * time.Second
	for _, operation := range []string{"新增驱动", "更新时区", "注册通道"} {
		t.Run(operation, func(t *testing.T) {
			logger := NewLog()
			driver := &delayedTimezoneDriver{block: time.UTC, entered: make(chan struct{}), release: make(chan struct{})}
			var child *Log
			if operation == "更新时区" {
				logger = NewLog(driver)
			}
			if operation == "注册通道" {
				child = NewLog(driver)
			}
			finished := make(chan error, 1)
			go func() {
				switch operation {
				case "新增驱动":
					finished <- logger.AddDriver(driver)
				case "注册通道":
					finished <- logger.RegisterChannel("child", child)
				default:
					logger.SetLocation(time.UTC)
					finished <- nil
				}
			}()
			select {
			case <-driver.entered:
			case <-time.After(completionTimeout):
				t.Fatal("未进入旧时区回调")
			}
			location := time.FixedZone("应用时区", int(8*time.Hour/time.Second))
			logger.SetLocation(location)
			close(driver.release)
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(completionTimeout):
				t.Fatal("时区通知无法收敛或发生死锁")
			}
			driver.mu.Lock()
			actual := driver.current
			driver.mu.Unlock()
			if actual != location || logger.now().Location() != location {
				t.Fatalf("旧通知覆盖最新配置: driver=%v logger=%v", actual, logger.now().Location())
			}
			if child != nil && child.now().Location() != location {
				t.Fatal("子通道未收敛到父日志器最新时区")
			}
		})
	}
}

type reentrantTimezoneDriver struct {
	timezoneCaptureDriver
	apply func()
}

func (driver *reentrantTimezoneDriver) SetLocation(*time.Location) { driver.apply() }

// TestLocationCallbackCanRegisterChannel 验证外部通知不占用状态锁或通道图锁。
func TestLocationCallbackCanRegisterChannel(t *testing.T) {
	parent := NewLog()
	driver := &reentrantTimezoneDriver{apply: func() {
		_ = parent.now()
		if err := parent.RegisterChannel("sibling", NewLog()); err != nil {
			t.Error(err)
		}
	}}
	finished := make(chan error, 1)
	go func() { finished <- parent.RegisterChannel("child", NewLog(driver)) }()
	const completionTimeout = 3 * time.Second
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(completionTimeout):
		t.Fatal("时区通知重入注册通道时死锁")
	}
}
