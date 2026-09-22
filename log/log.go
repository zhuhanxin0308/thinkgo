package log

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrLogClosed 表示日志器已关闭，不能再接受新的刷盘请求。
	ErrLogClosed = errors.New("日志器已关闭")
	// ErrNilLogDriver 表示日志器收到 nil 驱动。
	ErrNilLogDriver = errors.New("日志驱动不能为空")
	// ErrInvalidLogChannel 表示通道名称或实例无效。
	ErrInvalidLogChannel = errors.New("日志通道名称和实例不能为空")
	// ErrLogChannelExists 表示同名通道已经注册，禁止静默覆盖。
	ErrLogChannelExists = errors.New("日志通道已存在")
	// ErrLogChannelCycle 表示注册会形成通道引用环。
	ErrLogChannelCycle = errors.New("日志通道不能形成循环引用")
	// ErrLogChannelOwned 表示子通道已经归属于另一个父日志器。
	ErrLogChannelOwned = errors.New("日志通道已经归属于其他父日志器")
	// ErrLogAlreadyStarted 表示异步队列已经创建，固定容量参数不能再修改。
	ErrLogAlreadyStarted = errors.New("日志异步队列已经启动")
	// ErrInvalidOverflowPolicy 表示日志队列溢出策略不是受支持的值。
	ErrInvalidOverflowPolicy = errors.New("日志溢出策略无效")
	// ErrInvalidLogContext 表示有界日志入队收到 nil 上下文。
	ErrInvalidLogContext = errors.New("日志入队上下文不能为空")
	// ErrInvalidLogCloseContext 表示日志关停收到 nil 上下文。
	ErrInvalidLogCloseContext = errors.New("日志关停上下文不能为空")
	// ErrLogEntryDropped 表示日志条目未在有界等待内进入异步队列。
	ErrLogEntryDropped = errors.New("日志条目未被异步队列接受")
)

var channelRegistryMu sync.Mutex

const maxPendingLogErrors = 64

// OverflowPolicy 定义异步日志队列达到容量上限后的处理方式。
type OverflowPolicy uint8

const (
	// OverflowSync 保持默认兼容行为，在队列满时同步写入。
	OverflowSync OverflowPolicy = iota
	// OverflowDrop 在队列满时立即丢弃，并通过原子计数记录数量。
	OverflowDrop
)

const (
	// LevelEmergency 表示最高优先级的紧急日志。
	LevelEmergency = "emergency"
	// LevelAlert 表示需要立即关注的告警日志。
	LevelAlert = "alert"
	// LevelCritical 表示关键故障日志。
	LevelCritical = "critical"
	// LevelError 表示错误日志。
	LevelError = "error"
	// LevelWarning 表示警告日志。
	LevelWarning = "warning"
	// LevelNotice 表示提示性日志。
	LevelNotice = "notice"
	// LevelInfo 表示常规信息日志。
	LevelInfo = "info"
	// LevelDebug 表示调试日志。
	LevelDebug = "debug"
	// LevelSQL 表示 SQL 诊断日志。
	LevelSQL = "sql"
)

type levelSnapshot struct {
	allowAll bool
	levels   map[string]struct{}
}

// Log 日志管理器。
// 支持多驱动、上下文参数、调用位置记录、异步批量刷盘和关停保护。
type Log struct {
	stateMu          sync.RWMutex
	drivers          []Driver
	channels         map[string]*Log
	parent           *Log
	levels           atomic.Pointer[levelSnapshot]
	callerEnabled    bool
	asyncCh          chan *LogEntry
	flushCh          chan chan struct{}
	asyncDone        chan struct{}
	asyncOnce        sync.Once
	shutdownOnce     sync.Once
	closeDone        chan struct{}
	closing          chan struct{}
	producers        sync.WaitGroup
	inFlight         sync.WaitGroup
	bufferSize       int
	flushInterval    time.Duration
	fallbackWriter   io.Writer
	fallbackMu       sync.Mutex
	driverErrorCount int64
	errorMu          sync.Mutex
	pendingErrors    []error
	droppedErrors    uint64
	droppedEntries   uint64
	overflowPolicy   OverflowPolicy
	closeErr         error
	closed           bool
	location         *time.Location
}

// NewLog 创建日志管理器。
func NewLog(drivers ...Driver) *Log {
	logger := &Log{
		drivers:        append([]Driver(nil), drivers...),
		channels:       make(map[string]*Log),
		bufferSize:     200,
		flushInterval:  5 * time.Second,
		fallbackWriter: os.Stderr,
		closing:        make(chan struct{}),
		location:       time.UTC,
	}
	logger.levels.Store(newLevelSnapshot(nil))
	return logger
}

// SetLocation 设置日志条目和日志清理使用的应用时区，并同步到子通道和支持时区的驱动。
func (l *Log) SetLocation(location *time.Location) {
	if l == nil {
		return
	}
	if location == nil {
		location = time.UTC
	}
	l.stateMu.Lock()
	l.location = location
	drivers := append([]Driver(nil), l.drivers...)
	channels := make([]*Log, 0, len(l.channels))
	for _, channel := range l.channels {
		if channel != nil && channel != l {
			channels = append(channels, channel)
		}
	}
	l.stateMu.Unlock()

	for _, driver := range drivers {
		if locationAware, ok := driver.(LocationAwareDriver); ok {
			locationAware.SetLocation(location)
		}
	}
	for _, channel := range channels {
		channel.SetLocation(location)
	}
}

// now 返回按日志配置时区转换后的当前时间。
func (l *Log) now() time.Time {
	if l == nil {
		return time.Now().UTC()
	}
	l.stateMu.RLock()
	location := l.location
	l.stateMu.RUnlock()
	if location == nil {
		location = time.UTC
	}
	return time.Now().In(location)
}

// AddDriver 添加日志驱动。
func (l *Log) AddDriver(driver Driver) error {
	if isNilDriver(driver) {
		return ErrNilLogDriver
	}
	l.stateMu.Lock()
	if l.closed {
		l.stateMu.Unlock()
		return ErrLogClosed
	}
	l.drivers = append(l.drivers, driver)
	location := l.location
	l.stateMu.Unlock()
	if locationAware, ok := driver.(LocationAwareDriver); ok {
		locationAware.SetLocation(location)
	}
	return nil
}

// SetLevels 设置允许的日志级别。
func (l *Log) SetLevels(levels []string) {
	if l == nil {
		return
	}
	l.levels.Store(newLevelSnapshot(levels))
}

// IsLevelEnabled 判断日志级别是否启用，读取不可变快照时不获取互斥锁。
func (l *Log) IsLevelEnabled(level string) bool {
	if l == nil {
		return false
	}
	snapshot := l.levels.Load()
	if snapshot == nil || snapshot.allowAll {
		return true
	}
	_, enabled := snapshot.levels[normalizeLogLevel(level)]
	return enabled
}

// SetOverflowPolicy 设置异步队列溢出策略；队列启动后策略保持不变。
func (l *Log) SetOverflowPolicy(policy OverflowPolicy) error {
	if l == nil || (policy != OverflowSync && policy != OverflowDrop) {
		return ErrInvalidOverflowPolicy
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return ErrLogClosed
	}
	if l.asyncCh != nil {
		return ErrLogAlreadyStarted
	}
	l.overflowPolicy = policy
	return nil
}

// DroppedEntryCount 返回因显式 drop 策略或有界入队截止而拒绝的日志数量。
func (l *Log) DroppedEntryCount() uint64 {
	if l == nil {
		return 0
	}
	return atomic.LoadUint64(&l.droppedEntries)
}

// newLevelSnapshot 创建不会再被修改的级别快照，避免请求热路径重复规范化字符串。
func newLevelSnapshot(levels []string) *levelSnapshot {
	if len(levels) == 0 {
		return &levelSnapshot{allowAll: true}
	}
	allowed := make(map[string]struct{}, len(levels))
	for _, level := range levels {
		allowed[normalizeLogLevel(level)] = struct{}{}
	}
	return &levelSnapshot{levels: allowed}
}

func normalizeLogLevel(level string) string {
	return strings.ToLower(strings.TrimSpace(level))
}

// SetCallerEnabled 设置是否记录调用位置。
func (l *Log) SetCallerEnabled(enabled bool) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.callerEnabled = enabled
}

// SetBufferSize 设置异步缓冲区大小，仅能在首次记录或刷盘前调用。
func (l *Log) SetBufferSize(size int) error {
	if size <= 0 {
		return errors.New("日志缓冲区大小必须大于 0")
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return ErrLogClosed
	}
	if l.asyncCh != nil {
		return ErrLogAlreadyStarted
	}
	l.bufferSize = size
	return nil
}

// SetFlushInterval 设置定时刷盘间隔，仅能在首次记录或刷盘前调用。
func (l *Log) SetFlushInterval(d time.Duration) error {
	if d <= 0 {
		return errors.New("日志刷盘间隔必须大于 0")
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return ErrLogClosed
	}
	if l.asyncCh != nil {
		return ErrLogAlreadyStarted
	}
	l.flushInterval = d
	return nil
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

// RegisterChannel 注册命名日志通道，并拒绝无效、重复、已关闭或形成引用环的通道。
func (l *Log) RegisterChannel(name string, channel *Log) error {
	name = strings.TrimSpace(name)
	if name == "" || channel == nil {
		return ErrInvalidLogChannel
	}

	channelRegistryMu.Lock()
	defer channelRegistryMu.Unlock()
	if channel == l || logChannelReaches(channel, l) {
		return ErrLogChannelCycle
	}

	l.stateMu.Lock()
	if l.closed {
		l.stateMu.Unlock()
		return ErrLogClosed
	}
	channel.stateMu.Lock()
	channelClosed := channel.closed
	if channelClosed {
		channel.stateMu.Unlock()
		l.stateMu.Unlock()
		return ErrLogClosed
	}
	if channel.parent != nil && channel.parent != l {
		channel.stateMu.Unlock()
		l.stateMu.Unlock()
		return ErrLogChannelOwned
	}
	if _, exists := l.channels[name]; exists {
		channel.stateMu.Unlock()
		l.stateMu.Unlock()
		return fmt.Errorf("%w: %s", ErrLogChannelExists, name)
	}
	if l.channels == nil {
		l.channels = make(map[string]*Log)
	}
	l.channels[name] = channel
	channel.parent = l
	location := l.location
	channel.stateMu.Unlock()
	l.stateMu.Unlock()
	channel.SetLocation(location)
	return nil
}

func logChannelReaches(start *Log, target *Log) bool {
	visited := make(map[*Log]struct{})
	stack := []*Log{start}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current == target {
			return true
		}
		if _, exists := visited[current]; exists {
			continue
		}
		visited[current] = struct{}{}
		current.stateMu.RLock()
		for _, child := range current.channels {
			if child != nil {
				stack = append(stack, child)
			}
		}
		current.stateMu.RUnlock()
	}
	return false
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

// recordEntry 创建并记录一条日志条目。
func (l *Log) recordEntry(msg string, level string, ctx map[string]interface{}, callerSkip int) {
	if !l.IsLevelEnabled(level) {
		return
	}

	entry := newLogEntry(l.now(), level, msg, ctx)

	if l.isCallerEnabled() {
		if _, file, line, ok := runtime.Caller(callerSkip); ok {
			entry.Caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		}
	}

	l.ensureAsyncStarted()

	l.stateMu.RLock()
	if l.closed || l.asyncCh == nil {
		l.stateMu.RUnlock()
		return
	}

	select {
	case l.asyncCh <- entry:
		l.stateMu.RUnlock()
	default:
		if l.overflowPolicy == OverflowDrop {
			atomic.AddUint64(&l.droppedEntries, 1)
			l.stateMu.RUnlock()
			return
		}
		// 通道已满时降级为同步写入，避免高峰期丢日志。
		drivers := append([]Driver(nil), l.drivers...)
		fallbackWriter := l.fallbackWriter
		l.inFlight.Add(1)
		l.stateMu.RUnlock()
		defer l.inFlight.Done()
		l.writeEntryToDrivers(drivers, fallbackWriter, entry)
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
		if l.closing == nil {
			l.closing = make(chan struct{})
		}

		channelSize := l.bufferSize * 2
		if channelSize <= 0 {
			channelSize = 2
		}

		l.asyncCh = make(chan *LogEntry, channelSize)
		l.flushCh = make(chan chan struct{})
		l.asyncDone = make(chan struct{})
		go l.asyncFlushLoop()
	})
}

// writeEntryToDrivers 在不持有状态锁时调用驱动，避免外部 I/O 阻塞配置和关停流程。
func (l *Log) writeEntryToDrivers(drivers []Driver, fallbackWriter io.Writer, entry *LogEntry) {
	for _, driver := range drivers {
		if err := safeDriverWrite(driver, entry); err != nil {
			l.reportDriverError(driver, "write", err, fallbackWriter, 1)
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
		case flushed := <-l.flushCh:
			channelClosed := l.flushPending(buffer, bufferSize)
			buffer = make([]*LogEntry, 0, bufferSize)
			flushed <- struct{}{}
			if channelClosed {
				close(l.asyncDone)
				return
			}

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

// flushPending 固定接收屏障时的队列长度；后续生产者不会扩大本次排空窗口。
func (l *Log) flushPending(buffer []*LogEntry, bufferSize int) bool {
	channelClosed := false
	for remaining := len(l.asyncCh); remaining > 0; remaining-- {
		entry, ok := <-l.asyncCh
		if !ok {
			channelClosed = true
			break
		}
		buffer = append(buffer, entry)
		if len(buffer) >= bufferSize {
			l.flushToDrivers(buffer)
			buffer = make([]*LogEntry, 0, bufferSize)
		}
	}
	if len(buffer) > 0 {
		l.flushToDrivers(buffer)
	}
	return channelClosed
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

// flushToDrivers 将日志条目批量写入驱动，状态锁只用于生成不可变快照。
func (l *Log) flushToDrivers(entries []*LogEntry) {
	l.stateMu.RLock()
	drivers := append([]Driver(nil), l.drivers...)
	fallbackWriter := l.fallbackWriter
	l.stateMu.RUnlock()

	for _, driver := range drivers {
		if err := safeDriverSave(driver, entries); err != nil {
			l.reportDriverError(driver, "save", err, fallbackWriter, len(entries))
		}
	}
}

// Write 立即同步写入一条日志。
func (l *Log) Write(msg string, level string) {
	if !l.IsLevelEnabled(level) {
		return
	}

	entry := newLogEntry(l.now(), level, msg, nil)
	if l.isCallerEnabled() {
		if _, file, line, ok := runtime.Caller(1); ok {
			entry.Caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		}
	}

	drivers, fallbackWriter, accepted := l.beginSynchronousIO()
	if !accepted {
		return
	}
	defer l.inFlight.Done()
	l.writeEntryToDrivers(drivers, fallbackWriter, entry)
}

func (l *Log) beginSynchronousIO() ([]Driver, io.Writer, bool) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed {
		return nil, nil, false
	}
	l.inFlight.Add(1)
	return append([]Driver(nil), l.drivers...), l.fallbackWriter, true
}

// Record 记录日志到异步通道。
func (l *Log) Record(msg string, level string) {
	l.recordEntry(msg, level, nil, 2)
}

// Shutdown 保留旧名称但返回关闭错误；新代码应使用 Close。
func (l *Log) Shutdown() error {
	return l.Close()
}

// ErrorCtx 记录错误日志。
func (l *Log) ErrorCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, LevelError, ctx, 2)
}

// WarningCtx 记录警告日志。
func (l *Log) WarningCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, LevelWarning, ctx, 2)
}

// InfoCtx 记录信息日志。
func (l *Log) InfoCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, LevelInfo, ctx, 2)
}

// DebugCtx 记录调试日志。
func (l *Log) DebugCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, LevelDebug, ctx, 2)
}

// SqlCtx 记录带结构化上下文的 SQL 日志。
func (l *Log) SqlCtx(msg string, ctx map[string]interface{}) {
	l.recordEntry(msg, LevelSQL, ctx, 2)
}

// Errorf 格式化记录错误日志。
func (l *Log) Errorf(format string, args ...interface{}) {
	if !l.IsLevelEnabled(LevelError) {
		return
	}
	l.recordEntry(fmt.Sprintf(format, args...), LevelError, nil, 2)
}

// Warningf 格式化记录警告日志。
func (l *Log) Warningf(format string, args ...interface{}) {
	if !l.IsLevelEnabled(LevelWarning) {
		return
	}
	l.recordEntry(fmt.Sprintf(format, args...), LevelWarning, nil, 2)
}

// Infof 格式化记录信息日志。
func (l *Log) Infof(format string, args ...interface{}) {
	if !l.IsLevelEnabled(LevelInfo) {
		return
	}
	l.recordEntry(fmt.Sprintf(format, args...), LevelInfo, nil, 2)
}

// Debugf 格式化记录调试日志。
func (l *Log) Debugf(format string, args ...interface{}) {
	if !l.IsLevelEnabled(LevelDebug) {
		return
	}
	l.recordEntry(fmt.Sprintf(format, args...), LevelDebug, nil, 2)
}

// Sqlf 格式化记录 SQL 日志。
func (l *Log) Sqlf(format string, args ...interface{}) {
	if !l.IsLevelEnabled(LevelSQL) {
		return
	}
	l.recordEntry(fmt.Sprintf(format, args...), LevelSQL, nil, 2)
}

func (l *Log) Emergency(msg string) { l.recordEntry(msg, LevelEmergency, nil, 2) }
func (l *Log) Alert(msg string)     { l.recordEntry(msg, LevelAlert, nil, 2) }
func (l *Log) Critical(msg string)  { l.recordEntry(msg, LevelCritical, nil, 2) }
func (l *Log) Error(msg string)     { l.recordEntry(msg, LevelError, nil, 2) }
func (l *Log) Warning(msg string)   { l.recordEntry(msg, LevelWarning, nil, 2) }
func (l *Log) Notice(msg string)    { l.recordEntry(msg, LevelNotice, nil, 2) }
func (l *Log) Info(msg string)      { l.recordEntry(msg, LevelInfo, nil, 2) }
func (l *Log) Debug(msg string)     { l.recordEntry(msg, LevelDebug, nil, 2) }
func (l *Log) Sql(msg string)       { l.recordEntry(msg, LevelSQL, nil, 2) }

// reportDriverError 聚合驱动错误并写入兜底输出，避免磁盘故障时日志无声丢失。
func (l *Log) reportDriverError(driver Driver, action string, err error, fallbackWriter io.Writer, batchSize int) {
	if err == nil {
		return
	}

	atomic.AddInt64(&l.driverErrorCount, 1)
	wrapped := fmt.Errorf("日志驱动 %T 执行 %s 失败: %w", driver, action, err)
	l.storePendingError(wrapped)

	var builder strings.Builder
	builder.WriteString("[log-driver-error]")
	builder.WriteString(" action=" + action)
	builder.WriteString(fmt.Sprintf(" driver=%T", driver))
	builder.WriteString(" error=" + sanitizeDriverErrorText(err.Error()))
	if batchSize > 0 {
		builder.WriteString(fmt.Sprintf(" batch=%d", batchSize))
	}
	builder.WriteString("\n")

	if fallbackWriter == nil {
		fallbackWriter = os.Stderr
	}
	l.fallbackMu.Lock()
	fallbackErr := safeFallbackWrite(fallbackWriter, builder.String())
	l.fallbackMu.Unlock()
	if fallbackErr != nil {
		l.storePendingError(fmt.Errorf("日志兜底输出失败: %w", fallbackErr))
	}
}

func (l *Log) storePendingError(err error) {
	if err == nil {
		return
	}
	l.errorMu.Lock()
	defer l.errorMu.Unlock()
	if len(l.pendingErrors) >= maxPendingLogErrors {
		l.droppedErrors++
		return
	}
	l.pendingErrors = append(l.pendingErrors, err)
}

func (l *Log) takeDriverErrors() error {
	l.errorMu.Lock()
	defer l.errorMu.Unlock()
	errorsToJoin := append([]error(nil), l.pendingErrors...)
	if l.droppedErrors > 0 {
		errorsToJoin = append(errorsToJoin, fmt.Errorf("另有 %d 条日志驱动错误因数量限制被省略", l.droppedErrors))
	}
	joined := errors.Join(errorsToJoin...)
	l.pendingErrors = nil
	l.droppedErrors = 0
	return joined
}

func safeDriverWrite(driver Driver, entry *LogEntry) (err error) {
	if isNilDriver(driver) {
		return ErrNilLogDriver
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("日志驱动 WriteEntry 发生 panic: %v", recovered)
		}
	}()
	return driver.WriteEntry(entry.forDriver())
}

func safeDriverSave(driver Driver, entries []*LogEntry) (err error) {
	if isNilDriver(driver) {
		return ErrNilLogDriver
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("日志驱动 SaveEntries 发生 panic: %v", recovered)
		}
	}()
	isolation := make([]*LogEntry, len(entries))
	for index, entry := range entries {
		isolation[index] = entry.forDriver()
	}
	return driver.SaveEntries(isolation)
}

func safeDriverClose(driver Driver) (err error) {
	if isNilDriver(driver) {
		return ErrNilLogDriver
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("日志驱动 Close 发生 panic: %v", recovered)
		}
	}()
	return driver.Close()
}

func safeFallbackWrite(writer io.Writer, message string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("兜底输出发生 panic: %v", recovered)
		}
	}()
	written, err := io.WriteString(writer, message)
	if err != nil {
		return err
	}
	if written != len(message) {
		return io.ErrShortWrite
	}
	return nil
}

func isNilDriver(driver Driver) bool {
	if driver == nil {
		return true
	}
	value := reflect.ValueOf(driver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
