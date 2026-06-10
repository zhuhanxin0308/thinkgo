package log

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockDriver 模拟日志驱动（记录调用）
type mockDriver struct {
	saved   [][]*LogEntry // SaveEntries 被调用的记录
	written []*LogEntry   // WriteEntry 被调用的记录
	lock    sync.Mutex
}

type failingDriver struct {
	writeErr error
	saveErr  error
	closeErr error
}

func (d *failingDriver) SaveEntries(entries []*LogEntry) error {
	return d.saveErr
}

func (d *failingDriver) WriteEntry(entry *LogEntry) error {
	return d.writeErr
}

func (d *failingDriver) Close() error {
	return d.closeErr
}

func newMockDriver() *mockDriver {
	return &mockDriver{
		saved:   make([][]*LogEntry, 0),
		written: make([]*LogEntry, 0),
	}
}

func (d *mockDriver) SaveEntries(entries []*LogEntry) error {
	d.lock.Lock()
	defer d.lock.Unlock()
	copied := make([]*LogEntry, len(entries))
	copy(copied, entries)
	d.saved = append(d.saved, copied)
	return nil
}

func (d *mockDriver) WriteEntry(entry *LogEntry) error {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.written = append(d.written, entry)
	return nil
}

func (d *mockDriver) Close() error {
	return nil
}

func (d *mockDriver) totalSavedCount() int {
	d.lock.Lock()
	defer d.lock.Unlock()
	count := 0
	for _, batch := range d.saved {
		count += len(batch)
	}
	return count
}

func (d *mockDriver) allEntries() []*LogEntry {
	d.lock.Lock()
	defer d.lock.Unlock()
	all := make([]*LogEntry, 0)
	for _, batch := range d.saved {
		all = append(all, batch...)
	}
	return all
}

// TestLogAsyncFlush 验证异步批量刷盘
func TestLogAsyncFlush(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetBufferSize(5)
	logger.SetFlushInterval(100 * time.Millisecond)

	// 写入 3 条日志（不够 bufferSize）
	logger.Record("msg1", "info")
	logger.Record("msg2", "info")
	logger.Record("msg3", "info")

	// 等待定时刷盘触发
	time.Sleep(200 * time.Millisecond)

	count := driver.totalSavedCount()
	if count != 3 {
		t.Fatalf("定时刷盘后应有 3 条日志，实际 %d", count)
	}

	logger.Shutdown()
}

// TestLogAsyncBufferFull 验证缓冲区满触发刷盘
func TestLogAsyncBufferFull(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetBufferSize(5)
	logger.SetFlushInterval(10 * time.Second)

	// 写入 6 条日志（超过 bufferSize=5）
	for i := 0; i < 6; i++ {
		logger.Record("msg", "info")
	}

	// 等待异步处理
	time.Sleep(100 * time.Millisecond)

	count := driver.totalSavedCount()
	if count < 5 {
		t.Fatalf("缓冲区满时应至少刷出 5 条日志，实际 %d", count)
	}

	logger.Shutdown()
}

// TestLogLevelFilter 验证日志级别过滤
func TestLogLevelFilter(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetLevels([]string{"error", "warning"})
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Record("debug msg", "debug")     // 应被过滤
	logger.Record("info msg", "info")       // 应被过滤
	logger.Record("error msg", "error")     // 应通过
	logger.Record("warning msg", "warning") // 应通过

	// 等待刷盘
	time.Sleep(100 * time.Millisecond)

	count := driver.totalSavedCount()
	if count != 2 {
		t.Fatalf("过滤后应有 2 条日志，实际 %d", count)
	}

	logger.Shutdown()
}

// TestLogShutdownFlushesRemaining 验证 Shutdown 刷出剩余日志
func TestLogShutdownFlushesRemaining(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetBufferSize(100)
	logger.SetFlushInterval(10 * time.Second)

	logger.Record("msg1", "info")
	logger.Record("msg2", "info")

	// 立即关闭，应刷出剩余日志
	logger.Shutdown()

	count := driver.totalSavedCount()
	if count != 2 {
		t.Fatalf("Shutdown 后应刷出 2 条日志，实际 %d", count)
	}
}

// TestLogConcurrentSafety 验证并发写入安全性
func TestLogConcurrentSafety(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetBufferSize(50)
	logger.SetFlushInterval(50 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Record("concurrent msg", "info")
		}()
	}
	wg.Wait()

	logger.Shutdown()

	count := driver.totalSavedCount()
	if count != 100 {
		t.Fatalf("100 并发写入后应有 100 条日志，实际 %d", count)
	}
}

// TestLogHelperMethods 验证快捷方法
func TestLogHelperMethods(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Error("error msg")
	logger.Warning("warning msg")
	logger.Info("info msg")
	logger.Debug("debug msg")
	logger.Sql("sql msg")

	time.Sleep(100 * time.Millisecond)

	count := driver.totalSavedCount()
	if count != 5 {
		t.Fatalf("5 个快捷方法应产生 5 条日志，实际 %d", count)
	}

	logger.Shutdown()

	// 验证日志条目包含正确的级别
	allEntries := driver.allEntries()
	hasError := false
	for _, e := range allEntries {
		if strings.EqualFold(e.Level, "error") {
			hasError = true
		}
	}
	if !hasError {
		t.Fatal("日志条目应包含 error 级别")
	}
}

// TestLogEntryFormat 验证 LogEntry 格式化输出
func TestLogEntryFormat(t *testing.T) {
	entry := &LogEntry{
		Time:    time.Date(2026, 4, 7, 9, 30, 0, 0, time.Local),
		Level:   "error",
		Message: "测试消息",
		Caller:  "main.go:42",
		Context: map[string]interface{}{
			"user_id": 123,
		},
	}

	formatted := entry.FormatEntry()

	// 验证包含必要部分
	if !strings.Contains(formatted, "[ERROR]") {
		t.Error("格式化结果应包含 [ERROR]")
	}
	if !strings.Contains(formatted, "[main.go:42]") {
		t.Error("格式化结果应包含调用位置 [main.go:42]")
	}
	if !strings.Contains(formatted, "测试消息") {
		t.Error("格式化结果应包含消息")
	}
	if !strings.Contains(formatted, "user_id") {
		t.Error("格式化结果应包含上下文数据")
	}
}

// TestLogContextMethods 验证带上下文的日志方法
func TestLogContextMethods(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetFlushInterval(50 * time.Millisecond)

	ctx := map[string]interface{}{
		"user_id": 42,
		"action":  "login",
	}
	logger.ErrorCtx("用户登录失败", ctx)
	logger.InfoCtx("用户登录成功", ctx)

	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	count := driver.totalSavedCount()
	if count != 2 {
		t.Fatalf("应有 2 条带上下文日志，实际 %d", count)
	}

	// 验证上下文已保存
	entries := driver.allEntries()
	for _, e := range entries {
		if e.Context == nil {
			t.Fatal("日志条目的上下文不应为 nil")
		}
		if e.Context["user_id"] != 42 {
			t.Fatalf("上下文 user_id 应为 42, 实际 %v", e.Context["user_id"])
		}
	}
}

// TestLogFormatMethods 验证格式化日志方法
func TestLogFormatMethods(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Errorf("错误: %s, 代码: %d", "连接超时", 504)
	logger.Infof("用户 %d 登录", 123)

	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	entries := driver.allEntries()
	if len(entries) != 2 {
		t.Fatalf("应有 2 条格式化日志，实际 %d", len(entries))
	}

	// 验证格式化结果
	if !strings.Contains(entries[0].Message, "连接超时") {
		t.Error("格式化消息应包含 '连接超时'")
	}
	if !strings.Contains(entries[0].Message, "504") {
		t.Error("格式化消息应包含 '504'")
	}
}

// TestLogMultiDriver 验证多驱动同时写入
func TestLogMultiDriver(t *testing.T) {
	driver1 := newMockDriver()
	driver2 := newMockDriver()
	logger := NewLog(driver1)
	logger.AddDriver(driver2)
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Info("multi-driver test")

	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	// 两个驱动都应收到日志
	if driver1.totalSavedCount() != 1 {
		t.Fatalf("驱动1 应有 1 条日志，实际 %d", driver1.totalSavedCount())
	}
	if driver2.totalSavedCount() != 1 {
		t.Fatalf("驱动2 应有 1 条日志，实际 %d", driver2.totalSavedCount())
	}
}

// TestLogCallerEnabled 验证调用位置记录
func TestLogCallerEnabled(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetCallerEnabled(true)
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Info("caller test")

	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("应有 1 条日志，实际 %d", len(entries))
	}

	// 验证记录了调用位置
	if entries[0].Caller == "" {
		t.Error("启用 callerEnabled 后，日志条目应包含调用位置")
	}
	// 调用位置应类似 "log_test.go:xxx"
	if !strings.Contains(entries[0].Caller, "log_test.go") {
		t.Errorf("调用位置应包含 'log_test.go'，实际: %s", entries[0].Caller)
	}
}

// TestLogCallerDisabled 验证未启用时不记录调用位置
func TestLogCallerDisabled(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetCallerEnabled(false)
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Info("no caller test")

	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("应有 1 条日志，实际 %d", len(entries))
	}

	if entries[0].Caller != "" {
		t.Errorf("未启用 callerEnabled 时，调用位置应为空，实际: %s", entries[0].Caller)
	}
}

// TestLogSyncWrite 验证同步写入（Write 方法）
func TestLogSyncWrite(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)

	logger.Write("sync msg", "error")

	// 同步写入应立即到达 WriteEntry
	driver.lock.Lock()
	count := len(driver.written)
	driver.lock.Unlock()

	if count != 1 {
		t.Fatalf("同步写入后应有 1 条日志，实际 %d", count)
	}

	logger.Shutdown()
}

// TestLogContextSnapshot 验证异步日志会复制上下文，避免后续修改污染已入队日志。
func TestLogContextSnapshot(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetFlushInterval(50 * time.Millisecond)

	ctx := map[string]interface{}{
		"user_id": 7,
		"tags":    []interface{}{"origin", "stable"},
	}
	logger.InfoCtx("context snapshot", ctx)

	ctx["user_id"] = 99
	ctx["tags"].([]interface{})[0] = "mutated"

	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("应有 1 条日志，实际 %d", len(entries))
	}

	if entries[0].Context["user_id"] != 7 {
		t.Fatalf("日志上下文应保留入队时的 user_id=7，实际 %v", entries[0].Context["user_id"])
	}

	tags, ok := entries[0].Context["tags"].([]interface{})
	if !ok || len(tags) != 2 || tags[0] != "origin" {
		t.Fatalf("日志上下文中的切片应保留原始值，实际 %#v", entries[0].Context["tags"])
	}
}

// TestLogShutdownConcurrentWritersDoesNotPanic 验证关停与并发写日志重叠时不会触发 send on closed channel。
func TestLogShutdownConcurrentWritersDoesNotPanic(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetBufferSize(32)
	logger.SetFlushInterval(200 * time.Millisecond)

	start := make(chan struct{})
	panicCh := make(chan interface{}, 128)
	var wg sync.WaitGroup

	for index := 0; index < 64; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					panicCh <- recovered
				}
			}()

			<-start
			for count := 0; count < 20; count++ {
				logger.Info("shutdown-race")
			}
		}()
	}

	close(start)
	time.Sleep(20 * time.Millisecond)
	logger.Shutdown()
	wg.Wait()
	close(panicCh)

	for recovered := range panicCh {
		t.Fatalf("日志关停过程中不应发生 panic，实际为 %v", recovered)
	}
}

// TestLogDriverFailureFallsBackToStderr 验证驱动写入失败会被统计并输出到兜底通道。
func TestLogDriverFailureFallsBackToStderr(t *testing.T) {
	driver := &failingDriver{
		writeErr: errors.New("write failed"),
		saveErr:  errors.New("save failed"),
		closeErr: errors.New("close failed"),
	}
	logger := NewLog(driver)
	logger.SetFlushInterval(50 * time.Millisecond)

	var fallback bytes.Buffer
	logger.SetFallbackWriter(&fallback)

	logger.Info("driver failure")
	time.Sleep(100 * time.Millisecond)
	logger.Shutdown()

	if logger.DriverErrorCount() < 2 {
		t.Fatalf("驱动失败计数至少应累计写入和关闭错误，实际 %d", logger.DriverErrorCount())
	}

	output := fallback.String()
	if !strings.Contains(output, "save failed") || !strings.Contains(output, "close failed") {
		t.Fatalf("兜底输出应包含驱动错误详情，实际输出: %s", output)
	}
}
