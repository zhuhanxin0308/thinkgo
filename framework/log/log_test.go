package log

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
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

// blockingFailingDriver 用于稳定复现驱动 I/O 与日志状态写锁竞争时的死锁。
type blockingFailingDriver struct {
	entered chan struct{}
	release chan struct{}
}

type blockingSaveDriver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *blockingSaveDriver) SaveEntries(entries []*LogEntry) error {
	d.once.Do(func() { close(d.entered) })
	<-d.release
	return nil
}

func (d *blockingSaveDriver) WriteEntry(entry *LogEntry) error {
	return nil
}

func (d *blockingSaveDriver) Close() error {
	return nil
}

type panickingDriver struct{}

func (d *panickingDriver) SaveEntries(entries []*LogEntry) error {
	panic("save panic")
}

func (d *panickingDriver) WriteEntry(entry *LogEntry) error {
	panic("write panic")
}

func (d *panickingDriver) Close() error {
	panic("close panic")
}

type shortFallbackWriter struct{}

func (shortFallbackWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

func (d *blockingFailingDriver) SaveEntries(entries []*LogEntry) error {
	return nil
}

func (d *blockingFailingDriver) WriteEntry(entry *LogEntry) error {
	close(d.entered)
	<-d.release
	return errors.New("blocked write failed")
}

func (d *blockingFailingDriver) Close() error {
	return nil
}

type countingCloseDriver struct {
	closeErr error
	mu       sync.Mutex
	closed   int
}

func (d *countingCloseDriver) SaveEntries(entries []*LogEntry) error {
	return nil
}

func (d *countingCloseDriver) WriteEntry(entry *LogEntry) error {
	return nil
}

func (d *countingCloseDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed++
	return d.closeErr
}

func (d *countingCloseDriver) closeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

type logTestCredentials struct {
	Username string              `json:"username"`
	Password string              `json:"password"`
	Headers  http.Header         `json:"headers"`
	Metadata map[string][]string `json:"metadata"`
}

type logTestNode struct {
	Token string       `json:"token"`
	Next  *logTestNode `json:"next"`
}

type logSecretMapKey struct{}

func (logSecretMapKey) String() string {
	return "map-key-secret"
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

func closeTestLogger(t *testing.T, logger *Log) {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志器失败: %v", err)
	}
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

	closeTestLogger(t, logger)
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

	closeTestLogger(t, logger)
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

	closeTestLogger(t, logger)
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
	if err := logger.Shutdown(); err != nil {
		t.Fatalf("Shutdown 刷盘失败: %v", err)
	}

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

	closeTestLogger(t, logger)

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

	closeTestLogger(t, logger)

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

// TestLogEntryFormatRedactsSensitiveContext 验证格式化日志时会递归脱敏敏感上下文值。
func TestLogEntryFormatRedactsSensitiveContext(t *testing.T) {
	entry := &LogEntry{
		Time:    time.Date(2026, 4, 7, 9, 30, 0, 0, time.Local),
		Level:   "error",
		Message: "登录失败",
		Context: map[string]interface{}{
			"password": "pw-value-123",
			"nested": map[string]interface{}{
				"api_token": "token-value-456",
				"safe":      "visible-value",
			},
			"headers": []interface{}{
				map[string]interface{}{"Authorization": "Bearer secret-value-789"},
			},
		},
	}

	formatted := entry.FormatEntry()

	for _, secret := range []string{"pw-value-123", "token-value-456", "secret-value-789"} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("格式化日志不应包含敏感值 %q，实际为 %s", secret, formatted)
		}
	}
	if !strings.Contains(formatted, "[REDACTED]") {
		t.Fatalf("格式化日志应包含脱敏占位符，实际为 %s", formatted)
	}
	if !strings.Contains(formatted, "visible-value") {
		t.Fatalf("非敏感上下文应保留，实际为 %s", formatted)
	}
	if entry.Context["password"] != "pw-value-123" {
		t.Fatal("日志脱敏不应修改原始上下文")
	}
}

// TestLogEntryFormatEscapesControlCharacters 验证消息和元数据不能伪造额外日志行。
func TestLogEntryFormatEscapesControlCharacters(t *testing.T) {
	entry := &LogEntry{
		Time:    time.Date(2026, 4, 7, 9, 30, 0, 0, time.Local),
		Level:   "info\n[error]",
		Message: "正常消息\r\n[ERROR] 伪造消息\t结束",
		Caller:  "handler.go:10\n[CRITICAL]",
	}
	formatted := entry.FormatEntry()
	if strings.ContainsAny(formatted, "\r\n\t") {
		t.Fatalf("单条日志不应包含原始控制字符，实际为 %q", formatted)
	}
	for _, escaped := range []string{`\r`, `\n`, `\t`} {
		if !strings.Contains(formatted, escaped) {
			t.Fatalf("格式化日志应保留控制字符的转义表示 %q，实际为 %q", escaped, formatted)
		}
	}
}

// TestLogEntryFormatDoesNotInvokeCustomMapKeyStringer 验证非标量 map 键不能借 String 方法写入敏感内容。
func TestLogEntryFormatDoesNotInvokeCustomMapKeyStringer(t *testing.T) {
	entry := &LogEntry{
		Time:    time.Now(),
		Level:   "info",
		Message: "custom map key",
		Context: map[string]interface{}{
			"custom": map[logSecretMapKey]string{{}: "visible-value"},
		},
	}
	formatted := entry.FormatEntry()
	if strings.Contains(formatted, "map-key-secret") {
		t.Fatalf("格式化日志不应调用非标量 map 键的 String 方法，实际为 %s", formatted)
	}
	if !strings.Contains(formatted, logUnsupportedValue) || !strings.Contains(formatted, "visible-value") {
		t.Fatalf("非标量 map 键应安全降级并保留值，实际为 %s", formatted)
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
	closeTestLogger(t, logger)

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
	closeTestLogger(t, logger)

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
	if err := logger.AddDriver(driver2); err != nil {
		t.Fatalf("添加第二个日志驱动失败: %v", err)
	}
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Info("multi-driver test")

	time.Sleep(100 * time.Millisecond)
	closeTestLogger(t, logger)

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
	closeTestLogger(t, logger)

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
	closeTestLogger(t, logger)

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

	closeTestLogger(t, logger)
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
	closeTestLogger(t, logger)

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
	if err := logger.Shutdown(); err != nil {
		t.Fatalf("并发写入期间关闭日志器失败: %v", err)
	}
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
	shutdownErr := logger.Shutdown()
	if !errors.Is(shutdownErr, driver.saveErr) || !errors.Is(shutdownErr, driver.closeErr) {
		t.Fatalf("Shutdown 应返回保存和关闭错误，实际为 %v", shutdownErr)
	}

	if logger.DriverErrorCount() < 2 {
		t.Fatalf("驱动失败计数至少应累计写入和关闭错误，实际 %d", logger.DriverErrorCount())
	}

	output := fallback.String()
	if !strings.Contains(output, "save failed") || !strings.Contains(output, "close failed") {
		t.Fatalf("兜底输出应包含驱动错误详情，实际输出: %s", output)
	}
}

// TestLogDriverFallbackRedactsSecretsAndEscapesNewlines 验证兜底错误不会泄露凭据或注入伪造日志行。
func TestLogDriverFallbackRedactsSecretsAndEscapesNewlines(t *testing.T) {
	driver := &failingDriver{writeErr: errors.New("password=driver-secret\nforged")}
	logger := NewLog(driver)
	var fallback bytes.Buffer
	logger.SetFallbackWriter(&fallback)
	logger.Write("message-secret-must-not-be-repeated", "error")
	_ = logger.Close()

	output := fallback.String()
	for _, secret := range []string{"driver-secret", "message-secret-must-not-be-repeated"} {
		if strings.Contains(output, secret) {
			t.Fatalf("兜底输出不应包含敏感内容 %q，实际为 %q", secret, output)
		}
	}
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("兜底输出应只有框架追加的结尾换行，实际为 %q", output)
	}
	if !strings.Contains(output, logRedactedPlaceholder) {
		t.Fatalf("兜底输出应标记已脱敏字段，实际为 %q", output)
	}
}

// TestLogReturnsFallbackWriterFailure 验证兜底输出自身失败也能通过 Close 被观测。
func TestLogReturnsFallbackWriterFailure(t *testing.T) {
	logger := NewLog(&failingDriver{writeErr: errors.New("primary failure")})
	logger.SetFallbackWriter(shortFallbackWriter{})
	logger.Write("trigger", "error")
	err := logger.Close()
	if !errors.Is(err, io.ErrShortWrite) || !strings.Contains(err.Error(), "primary failure") {
		t.Fatalf("Close 应同时返回原始驱动错误和兜底短写错误，实际为 %v", err)
	}
}

// TestLogDriverFailureDoesNotDeadlockWithQueuedWriter 验证驱动失败回报不会在写锁排队时重入读锁。
func TestLogDriverFailureDoesNotDeadlockWithQueuedWriter(t *testing.T) {
	driver := &blockingFailingDriver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	logger := NewLog(driver)
	logger.SetFallbackWriter(&bytes.Buffer{})

	writeDone := make(chan struct{})
	go func() {
		logger.Write("sensitive message", "error")
		close(writeDone)
	}()

	select {
	case <-driver.entered:
	case <-time.After(time.Second):
		t.Fatal("同步写入未进入驱动")
	}

	setStarted := make(chan struct{})
	setDone := make(chan struct{})
	go func() {
		close(setStarted)
		logger.SetLevels([]string{"error"})
		close(setDone)
	}()
	<-setStarted

	// 旧实现会让 SetLevels 排队，新实现会立即完成；两种路径都释放驱动以验证写入能退出。
	select {
	case <-setDone:
	case <-time.After(30 * time.Millisecond):
	}

	close(driver.release)
	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("驱动失败回报与排队写锁发生死锁")
	}
	select {
	case <-setDone:
	case <-time.After(time.Second):
		t.Fatal("驱动写入结束后状态写锁仍未释放")
	}

	if err := logger.Close(); err == nil || !strings.Contains(err.Error(), "blocked write failed") {
		t.Fatalf("Close 应返回此前尚未消费的驱动写入错误，实际为 %v", err)
	}
}

// TestLogFlushDrainsPendingEntriesAndReturnsDriverErrors 验证显式刷盘既提供完成屏障，也传播驱动错误。
func TestLogFlushDrainsPendingEntriesAndReturnsDriverErrors(t *testing.T) {
	saveErr := errors.New("flush save failed")
	driver := &failingDriver{saveErr: saveErr}
	logger := NewLog(driver)
	logger.SetFlushInterval(time.Hour)
	logger.Info("pending entry")

	if err := logger.Flush(context.Background()); !errors.Is(err, saveErr) {
		t.Fatalf("Flush 应返回驱动保存错误，实际为 %v", err)
	}
	if logger.DriverErrorCount() != 1 {
		t.Fatalf("Flush 后驱动错误计数应为 1，实际为 %d", logger.DriverErrorCount())
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("已由 Flush 消费的错误不应在 Close 中重复返回: %v", err)
	}
}

// TestLogFlushCancellationDoesNotBlockAsyncLoop 验证调用方超时后异步循环仍能完成原刷盘请求并继续服务。
func TestLogFlushCancellationDoesNotBlockAsyncLoop(t *testing.T) {
	driver := &blockingSaveDriver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	logger := NewLog(driver)
	logger.Info("blocked flush")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := logger.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时刷盘应返回 context.DeadlineExceeded，实际为 %v", err)
	}
	select {
	case <-driver.entered:
	default:
		t.Fatal("超时前刷盘请求应已进入驱动")
	}
	close(driver.release)
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatalf("前一次调用方超时后日志循环应继续响应，实际为 %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志器失败: %v", err)
	}
}

// TestLogConvertsDriverPanicsToErrors 验证扩展驱动的 panic 不会击穿业务协程。
func TestLogConvertsDriverPanicsToErrors(t *testing.T) {
	logger := NewLog(&panickingDriver{})
	logger.SetFallbackWriter(io.Discard)
	logger.Write("sync panic", "error")
	logger.Info("async panic")
	flushErr := logger.Flush(context.Background())
	if flushErr == nil || !strings.Contains(flushErr.Error(), "write panic") || !strings.Contains(flushErr.Error(), "save panic") {
		t.Fatalf("Flush 应聚合驱动写入和保存 panic，实际为 %v", flushErr)
	}
	closeErr := logger.Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "close panic") {
		t.Fatalf("Close 应返回驱动关闭 panic，实际为 %v", closeErr)
	}
}

// TestLogCloseAggregatesErrorsAndIsIdempotent 验证关闭错误不会被吞掉，且并发安全关闭只执行一次。
func TestLogCloseAggregatesErrorsAndIsIdempotent(t *testing.T) {
	firstErr := errors.New("first close failed")
	secondErr := errors.New("second close failed")
	first := &countingCloseDriver{closeErr: firstErr}
	second := &countingCloseDriver{closeErr: secondErr}
	logger := NewLog(first, second)

	err := logger.Close()
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Close 应聚合所有驱动关闭错误，实际为 %v", err)
	}
	secondCloseErr := logger.Close()
	if !errors.Is(secondCloseErr, firstErr) || !errors.Is(secondCloseErr, secondErr) {
		t.Fatalf("重复 Close 应返回首次关闭结果，实际为 %v", secondCloseErr)
	}
	if first.closeCount() != 1 || second.closeCount() != 1 {
		t.Fatalf("每个驱动只能关闭一次，实际为 first=%d second=%d", first.closeCount(), second.closeCount())
	}
}

// TestLogAddDriverRejectsNilAndClosedLogger 验证运行期驱动注册错误不会被静默忽略。
func TestLogAddDriverRejectsNilAndClosedLogger(t *testing.T) {
	logger := NewLog()
	var typedNil *mockDriver
	if err := logger.AddDriver(typedNil); !errors.Is(err, ErrNilLogDriver) {
		t.Fatalf("类型化 nil 驱动应返回 ErrNilLogDriver，实际为 %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志器失败: %v", err)
	}
	if err := logger.AddDriver(newMockDriver()); !errors.Is(err, ErrLogClosed) {
		t.Fatalf("关闭后添加驱动应返回 ErrLogClosed，实际为 %v", err)
	}
}

// TestLogBoundsPendingDriverErrors 验证持续驱动故障不会让待返回错误列表无限增长。
func TestLogBoundsPendingDriverErrors(t *testing.T) {
	driver := &failingDriver{writeErr: errors.New("persistent failure")}
	logger := NewLog(driver)
	logger.SetFallbackWriter(io.Discard)
	for index := 0; index < maxPendingLogErrors+25; index++ {
		logger.Write("failure", "error")
	}

	logger.errorMu.Lock()
	pendingCount := len(logger.pendingErrors)
	logger.errorMu.Unlock()
	if pendingCount > maxPendingLogErrors {
		t.Fatalf("待返回驱动错误最多保留 %d 条，实际为 %d", maxPendingLogErrors, pendingCount)
	}
	err := logger.Close()
	if err == nil || !strings.Contains(err.Error(), "省略") {
		t.Fatalf("Close 应说明被限流省略的错误数量，实际为 %v", err)
	}
}

// TestLogAsyncSettingsRejectInvalidOrLateChanges 验证异步队列参数不会被静默修正或在启动后假装生效。
func TestLogAsyncSettingsRejectInvalidOrLateChanges(t *testing.T) {
	logger := NewLog(newMockDriver())
	if err := logger.SetBufferSize(0); err == nil {
		t.Fatal("非正缓冲区大小应返回错误")
	}
	if err := logger.SetFlushInterval(0); err == nil {
		t.Fatal("非正刷盘间隔应返回错误")
	}
	if err := logger.SetBufferSize(8); err != nil {
		t.Fatalf("启动前设置缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Second); err != nil {
		t.Fatalf("启动前设置刷盘间隔失败: %v", err)
	}
	logger.Info("start async loop")
	if err := logger.SetBufferSize(16); !errors.Is(err, ErrLogAlreadyStarted) {
		t.Fatalf("启动后修改缓冲区应返回 ErrLogAlreadyStarted，实际为 %v", err)
	}
	if err := logger.SetFlushInterval(2 * time.Second); !errors.Is(err, ErrLogAlreadyStarted) {
		t.Fatalf("启动后修改刷盘间隔应返回 ErrLogAlreadyStarted，实际为 %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志器失败: %v", err)
	}
	if err := logger.SetBufferSize(4); !errors.Is(err, ErrLogClosed) {
		t.Fatalf("关闭后修改缓冲区应返回 ErrLogClosed，实际为 %v", err)
	}
}

// TestLogLevelSnapshot 验证级别集合会规范化、快照读取稳定且空集合保持放行兼容语义。
func TestLogLevelSnapshot(t *testing.T) {
	logger := NewLog()
	logger.SetLevels([]string{" ERROR ", "Warning"})
	if !logger.IsLevelEnabled("error") || !logger.IsLevelEnabled("WARNING") {
		t.Fatal("规范化后的日志级别应允许大小写不同的查询")
	}
	if logger.IsLevelEnabled("info") || logger.IsLevelEnabled("unknown") {
		t.Fatal("未配置的日志级别不应被放行")
	}
	logger.SetLevels(nil)
	if !logger.IsLevelEnabled("unknown") {
		t.Fatal("空日志级别列表应保持现有的全部放行语义")
	}

	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			for round := 0; round < 100; round++ {
				if index%2 == 0 {
					logger.SetLevels([]string{"error", "warning"})
				} else {
					logger.SetLevels(nil)
				}
				_ = logger.IsLevelEnabled("info")
			}
		}(index)
	}
	wait.Wait()
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭级别测试日志器失败: %v", err)
	}
}
