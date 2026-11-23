package log

import (
	"testing"
	"testing/synctest"
	"time"
)

// TestLogAsyncFlush 检查未满批次的日志只在第一个刷盘周期到达时交给驱动。
func TestLogAsyncFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = time.Second
		const pending = 3
		driver := newMockDriver()
		logger := NewLog(driver)
		defer closeTestLogger(t, logger)
		if err := logger.SetBufferSize(pending + 1); err != nil {
			t.Fatal(err)
		}
		if err := logger.SetFlushInterval(interval); err != nil {
			t.Fatal(err)
		}
		for range pending {
			logger.Info("等待定时刷盘")
		}
		synctest.Wait()
		time.Sleep(interval - time.Nanosecond)
		synctest.Wait()
		if got := driver.totalSavedCount(); got != 0 {
			t.Fatalf("第一个周期尚未结束，已刷盘 %d 条", got)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := driver.totalSavedCount(); got != pending {
			t.Fatalf("第一个周期到达时已刷盘 %d 条，期望 %d 条", got, pending)
		}
	})
}

// TestLogAsyncBufferFull 在虚拟时间不前进时验证第五条日志触发整批刷盘。
func TestLogAsyncBufferFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const batchSize = 5
		driver := newMockDriver()
		logger := NewLog(driver)
		defer closeTestLogger(t, logger)
		if err := logger.SetBufferSize(batchSize); err != nil {
			t.Fatal(err)
		}
		for range batchSize - 1 {
			logger.Info("尚未满批")
		}
		synctest.Wait()
		if got := driver.totalSavedCount(); got != 0 {
			t.Fatalf("未满批时已刷盘 %d 条", got)
		}
		logger.Info("填满批次")
		synctest.Wait()
		if got := driver.totalSavedCount(); got != batchSize {
			t.Fatalf("满批时已刷盘 %d 条，期望 %d 条", got, batchSize)
		}
		logger.Info("下一批首条")
		synctest.Wait()
		if got := driver.totalSavedCount(); got != batchSize {
			t.Fatalf("下一批尚未填满，累计已刷盘 %d 条", got)
		}
	})
}

type replenishingFlushDriver struct {
	logger *Log
	counts []int
}

func (driver *replenishingFlushDriver) SaveEntries(entries []*LogEntry) error {
	driver.counts = append(driver.counts, len(entries))
	// 模拟驱动 I/O 期间仍有新日志到达；这些日志不应无限扩大当前 Flush 的排空窗口。
	driver.logger.asyncCh <- &LogEntry{Message: "后到日志"}
	return nil
}
func (*replenishingFlushDriver) WriteEntry(*LogEntry) error { return nil }
func (*replenishingFlushDriver) Close() error               { return nil }

// TestFlushPendingHasFiniteWindowAndBatches 验证显式屏障按固定快照排空，并遵守普通批次上限。
func TestFlushPendingHasFiniteWindowAndBatches(t *testing.T) {
	const batchSize = 2
	const accepted = 4
	driver := &replenishingFlushDriver{}
	logger := NewLog(driver)
	driver.logger = logger
	logger.asyncCh = make(chan *LogEntry, accepted)
	for index := 0; index < accepted; index++ {
		logger.asyncCh <- &LogEntry{Message: "屏障前日志"}
	}
	if logger.flushPending(nil, batchSize) {
		t.Fatal("正常队列误判为已关闭")
	}
	if len(driver.counts) != 2 || driver.counts[0] != batchSize || driver.counts[1] != batchSize {
		t.Fatalf("Flush 未遵守批次上限: %v", driver.counts)
	}
	if remaining := len(logger.asyncCh); remaining != 2 {
		t.Fatalf("Flush 消费了屏障后的持续输入: %d", remaining)
	}
}
