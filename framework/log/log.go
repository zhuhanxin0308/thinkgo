package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Log 日志管理器。
// 支持多驱动、上下文参数、调用位置记录、异步批量刷盘和关停保护。
type Log struct {
	stateMu          sync.RWMutex
	drivers          []Driver
	channels         map[string]*Log
	levels           []string
	callerEnabled    bool
	asyncCh          chan *LogEntry
	asyncDone        chan struct{}
	asyncOnce        sync.Once
	shutdownOnce     sync.Once
	bufferSize       int
	flushInterval    time.Duration
	fallbackWriter   io.Writer
	driverErrorCount int64
	closed           bool
}

// NewLog 创建日志管理器。
func NewLog(drivers ...Driver) *Log {
	return &Log{
		drivers:        drivers,
		channels:       make(map[string]*Log),
		levels:         make([]string, 0),
		bufferSize:     200,
		flushInterval:  5 * time.Second,
		fallbackWriter: os.Stderr,
	}
}

// AddDriver 添加日志驱动。
func (l *Log) AddDriver(driver Driver) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return
	}
	l.drivers = append(l.drivers, driver)
}

// SetLevels 设置允许的日志级别。
func (l *Log) SetLevels(levels []string) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.levels = append([]string(nil), levels...)
}

// SetCallerEnabled 设置是否记录调用位置。
func (l *Log) SetCallerEnabled(enabled bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.callerEnabled = enabled
}

// SetBufferSize 设置异步缓冲区大小。
func (l *Log) SetBufferSize(size int) {
	if size <= 0 {
		size = 1
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.bufferSize = size
}

// SetFlushInterval 设置定时刷盘间隔。
func (l *Log) SetFlushInterval(d time.Duration) {
	if d <= 0 {
		d = time.Second
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.flushInterval = d
}

// SetFallbackWriter 设置驱动失败时的兜底输出目标。
func (l *Log) SetFallbackWriter(writer io.Writer) {
	if writer == nil {
		writer = os.Stderr
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.fallbackWriter = writer
}

// RegisterChannel 注册命名日志通道，支持把不同类型日志隔离到不同驱动。
func (l *Log) RegisterChannel(name string, channel *Log) {
	if name == "" || channel == nil {
		return
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.channels[name] = channel
}

// Channel 返回指定命名通道，未找到时回退到当前日志器，保证调用方无需额外判空。
func (l *Log) Channel(name string) *Log {
	if name == "" {
		return l
	}
	l.stateMu.RLock()
	channel, ok := l.channels[name]
	l.stateMu.RUnlock()
	if !ok || channel == nil {
		return l
	}
	return channel
}

// DriverErrorCount 返回驱动失败累计次数。
func (l *Log) DriverErrorCount() int64 {
	return atomic.LoadInt64(&l.driverErrorCount)
}

// isLevelAllowed 检查日志级别是否允许。
func (l *Log) isLevelAllowed(level string) bool {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()

	if len(l.levels) == 0 {
		return true
	}

	for _, value := range l.levels {
		if strings.EqualFold(value, level) {
			return true
		}
	}
	return false
}

// recordEntry 创建并记录一条日志条目。
func (l *Log) recordEntry(msg string, level string, ctx map[string]interface{}, callerSkip int) {
	if !l.isLevelAllowed(level) {
		return
	}

	entry := &LogEntry{
		Time:    time.Now(),
		Level:   level,
		Message: msg,
		Context: cloneContext(ctx),
	}

	if l.isCallerEnabled() {
		if _, file, line, ok := runtime.Caller(callerSkip); ok {
			entry.Caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		}
	}

	l.ensureAsyncStarted()

	l.stateMu.RLock()
	defer l.stateMu.RUnlock()

	if l.closed || l.asyncCh == nil {
		return
	}

	select {
	case l.asyncCh <- entry:
	default:
		// 通道已满时降级为同步写入，避免高峰期丢日志。
		l.syncWriteEntryLocked(entry)
	}
}

func (l *Log) isCallerEnabled() bool {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	return l.callerEnabled
}

// ensureAsyncStarted 懒启动异步刷盘协程，并在关闭后禁止再次启动。
func (l *Log) ensureAsyncStarted() {
	l.asyncOnce.Do(func() {
		l.stateMu.Lock()
		defer l.stateMu.Unlock()

		if l.closed {
			return
		}

		channelSize := l.bufferSize * 2
		if channelSize <= 0 {
			channelSize = 2
		}

		l.asyncCh = make(chan *LogEntry, channelSize)
		l.asyncDone = make(chan struct{})
		go l.asyncFlushLoop()
	})
}

// syncWriteEntryLocked 在已持有读锁的前提下同步写入单条日志。
func (l *Log) syncWriteEntryLocked(entry *LogEntry) {
	for _, driver := range l.drivers {
		if err := driver.WriteEntry(entry); err != nil {
			l.reportDriverError(driver, "write", err, entry, 1)
		}
	}
}

// asyncFlushLoop 在后台批量刷盘日志。
func (l *Log) asyncFlushLoop() {
	bufferSize := l.getBufferSize()
	buffer := make([]*LogEntry, 0, bufferSize)
	ticker := time.NewTicker(l.getFlushInterval())
	defer ticker.Stop()

	for {
		select {
		case entry, ok := <-l.asyncCh:
			if !ok {
				if len(buffer) > 0 {
					l.flushToDrivers(buffer)
				}
				close(l.asyncDone)
				return
			}

			buffer = append(buffer, entry)
			if len(buffer) >= bufferSize {
				l.flushToDrivers(buffer)
				buffer = make([]*LogEntry, 0, bufferSize)
			}

		case <-ticker.C:
			if len(buffer) > 0 {
				l.flushToDrivers(buffer)
				buffer = make([]*LogEntry, 0, bufferSize)
			}
		}
	}
}

func (l *Log) getBufferSize() int {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.bufferSize <= 0 {
		return 1
	}
	return l.bufferSize
}

func (l *Log) getFlushInterval() time.Duration {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.flushInterval <= 0 {
		return time.Second
	}
	return l.flushInterval
}

// flushToDrivers 将日志条目批量写入所有驱动。
func (l *Log) flushToDrivers(entries []*LogEntry) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()

	for _, driver := range l.drivers {
		if err := driver.SaveEntries(entries); err != nil {
			l.reportDriverError(driver, "save", err, nil, len(entries))
		}
	}
}

// Write 立即同步写入一条日志。
func (l *Log) Write(msg string, level string) {
	if !l.isLevelAllowed(level) {
		return
	}

	entry := &LogEntry{
		Time:    time.Now(),
		Level:   level,
		Message: msg,
	}
	if l.isCallerEnabled() {
		if _, file, line, ok := runtime.Caller(1); ok {
			entry.Caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		}
	}

	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed {
		return
	}
	l.syncWriteEntryLocked(entry)
}

// Record 记录日志到异步通道。
func (l *Log) Record(msg string, level string) {
	l.recordEntry(msg, level, nil, 2)
}

// Save 为兼容旧接口保留，无需显式调用。
func (l *Log) Save() {}

// Shutdown 优雅关闭日志系统，避免与并发写入发生 send on closed channel。
func (l *Log) Shutdown() {
	l.shutdownOnce.Do(func() {
		l.stateMu.Lock()
		l.closed = true
		asyncCh := l.asyncCh
		asyncDone := l.asyncDone
		channels := make([]*Log, 0, len(l.channels))
		for _, channel := range l.channels {
			if channel != nil && channel != l {
				channels = append(channels, channel)
			}
		}
		l.stateMu.Unlock()

		if asyncCh != nil {
			close(asyncCh)
		}
		if asyncDone != nil {
			<-asyncDone
		}

		l.stateMu.RLock()
		drivers := append([]Driver(nil), l.drivers...)
		l.stateMu.RUnlock()
		for _, driver := range drivers {
			if err := driver.Close(); err != nil {
				l.reportDriverError(driver, "close", err, nil, 0)
			}
		}
		for _, channel := range channels {
			channel.Shutdown()
		}
	})
}

// ErrorCtx 记录错误日志。
func (l *Log) ErrorCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, "error", ctx, 2)
}

// WarningCtx 记录警告日志。
func (l *Log) WarningCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, "warning", ctx, 2)
}

// InfoCtx 记录信息日志。
func (l *Log) InfoCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, "info", ctx, 2)
}

// DebugCtx 记录调试日志。
func (l *Log) DebugCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, "debug", ctx, 2)
}

// Errorf 格式化记录错误日志。
func (l *Log) Errorf(format string, args ...interface{}) {
	l.recordEntry(fmt.Sprintf(format, args...), "error", nil, 2)
}

// Warningf 格式化记录警告日志。
func (l *Log) Warningf(format string, args ...interface{}) {
	l.recordEntry(fmt.Sprintf(format, args...), "warning", nil, 2)
}

// Infof 格式化记录信息日志。
func (l *Log) Infof(format string, args ...interface{}) {
	l.recordEntry(fmt.Sprintf(format, args...), "info", nil, 2)
}

// Debugf 格式化记录调试日志。
func (l *Log) Debugf(format string, args ...interface{}) {
	l.recordEntry(fmt.Sprintf(format, args...), "debug", nil, 2)
}

// Sqlf 格式化记录 SQL 日志。
func (l *Log) Sqlf(format string, args ...interface{}) {
	l.recordEntry(fmt.Sprintf(format, args...), "sql", nil, 2)
}

func (l *Log) Emergency(msg string) { l.recordEntry(msg, "emergency", nil, 2) }
func (l *Log) Alert(msg string)     { l.recordEntry(msg, "alert", nil, 2) }
func (l *Log) Critical(msg string)  { l.recordEntry(msg, "critical", nil, 2) }
func (l *Log) Error(msg string)     { l.recordEntry(msg, "error", nil, 2) }
func (l *Log) Warning(msg string)   { l.recordEntry(msg, "warning", nil, 2) }
func (l *Log) Notice(msg string)    { l.recordEntry(msg, "notice", nil, 2) }
func (l *Log) Info(msg string)      { l.recordEntry(msg, "info", nil, 2) }
func (l *Log) Debug(msg string)     { l.recordEntry(msg, "debug", nil, 2) }
func (l *Log) Sql(msg string)       { l.recordEntry(msg, "sql", nil, 2) }

// reportDriverError 聚合驱动错误并写入兜底输出，避免磁盘故障时日志无声丢失。
func (l *Log) reportDriverError(driver Driver, action string, err error, entry *LogEntry, batchSize int) {
	if err == nil {
		return
	}

	atomic.AddInt64(&l.driverErrorCount, 1)

	var builder strings.Builder
	builder.WriteString("[log-driver-error]")
	builder.WriteString(" action=" + action)
	builder.WriteString(fmt.Sprintf(" driver=%T", driver))
	builder.WriteString(" error=" + err.Error())
	if entry != nil {
		builder.WriteString(" message=" + entry.Message)
	}
	if batchSize > 0 {
		builder.WriteString(fmt.Sprintf(" batch=%d", batchSize))
	}
	builder.WriteString("\n")

	l.stateMu.RLock()
	fallbackWriter := l.fallbackWriter
	l.stateMu.RUnlock()
	if fallbackWriter == nil {
		fallbackWriter = os.Stderr
	}
	_, _ = io.WriteString(fallbackWriter, builder.String())
}

// cloneContext 在入队前复制上下文，避免异步刷盘阶段读取到已被修改的 map。
func cloneContext(ctx map[string]interface{}) map[string]interface{} {
	if len(ctx) == 0 {
		return nil
	}

	cloned := make(map[string]interface{}, len(ctx))
	for key, value := range ctx {
		cloned[key] = cloneContextValue(value)
	}
	return cloned
}

func cloneContextValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneContext(typed)
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		for key, innerValue := range typed {
			cloned[key] = innerValue
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneContextValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []int:
		return append([]int(nil), typed...)
	case []int64:
		return append([]int64(nil), typed...)
	case []float64:
		return append([]float64(nil), typed...)
	case []bool:
		return append([]bool(nil), typed...)
	default:
		return value
	}
}
