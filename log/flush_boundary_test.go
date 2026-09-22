package log

import "testing"

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
