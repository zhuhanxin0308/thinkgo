package log

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingStringer struct {
	calls atomic.Int32
}

func (s *countingStringer) String() string {
	s.calls.Add(1)
	return "formatted"
}

// TestDisabledFormattedLogDoesNotStringify 验证禁用级别会在格式化参数前立即返回。
func TestDisabledFormattedLogDoesNotStringify(t *testing.T) {
	logger := NewLog()
	logger.SetLevels([]string{"error"})
	value := &countingStringer{}
	logger.Infof("value=%s", value)
	logger.Debugf("value=%s", value)
	if calls := value.calls.Load(); calls != 0 {
		t.Fatalf("禁用日志级别不应调用 String，实际调用 %d 次", calls)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭格式化过滤测试日志器失败: %v", err)
	}
}

type overflowTestDriver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	writes  atomic.Int32
}

func (d *overflowTestDriver) SaveEntries([]*LogEntry) error {
	d.once.Do(func() { close(d.entered) })
	<-d.release
	return nil
}

func (d *overflowTestDriver) WriteEntry(*LogEntry) error {
	d.writes.Add(1)
	return nil
}

func (d *overflowTestDriver) Close() error { return nil }

func fillLogQueueUntilSaveBlocked(t *testing.T, logger *Log, driver *overflowTestDriver) {
	t.Helper()
	if err := logger.SetBufferSize(1); err != nil {
		t.Fatalf("设置最小日志缓冲区失败: %v", err)
	}
	logger.SetFlushInterval(time.Hour)
	logger.Record("seed", "info")
	select {
	case <-driver.entered:
	case <-time.After(time.Second):
		t.Fatal("异步日志未进入阻塞保存驱动")
	}
	logger.Record("queued-1", "info")
	logger.Record("queued-2", "info")
}

// TestLogOverflowDefaultsToSynchronousWrite 验证默认溢出策略保持同步写入兼容行为。
func TestLogOverflowDefaultsToSynchronousWrite(t *testing.T) {
	driver := &overflowTestDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := NewLog(driver)
	fillLogQueueUntilSaveBlocked(t, logger, driver)
	logger.Record("overflow", "info")
	if writes := driver.writes.Load(); writes != 1 {
		t.Fatalf("默认队列溢出应同步写入一次，实际为 %d", writes)
	}
	close(driver.release)
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭同步溢出测试日志器失败: %v", err)
	}
}

// TestLogOverflowDropIsExplicitAndBounded 验证只有显式 drop 策略才丢弃，并准确记录丢弃数量。
func TestLogOverflowDropIsExplicitAndBounded(t *testing.T) {
	driver := &overflowTestDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := NewLog(driver)
	if err := logger.SetOverflowPolicy(OverflowDrop); err != nil {
		t.Fatalf("设置 drop 溢出策略失败: %v", err)
	}
	if err := logger.SetOverflowPolicy(OverflowPolicy(99)); !errors.Is(err, ErrInvalidOverflowPolicy) {
		t.Fatalf("非法溢出策略应返回 ErrInvalidOverflowPolicy，实际为 %v", err)
	}
	fillLogQueueUntilSaveBlocked(t, logger, driver)
	logger.Record("overflow", "info")
	if writes := driver.writes.Load(); writes != 0 {
		t.Fatalf("drop 策略不应同步写入溢出条目，实际为 %d", writes)
	}
	if dropped := logger.DroppedEntryCount(); dropped != 1 {
		t.Fatalf("drop 策略丢弃计数应为 1，实际为 %d", dropped)
	}
	if err := logger.SetOverflowPolicy(OverflowSync); !errors.Is(err, ErrLogAlreadyStarted) {
		t.Fatalf("启动后修改溢出策略应返回 ErrLogAlreadyStarted，实际为 %v", err)
	}
	close(driver.release)
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭 drop 溢出测试日志器失败: %v", err)
	}
}
