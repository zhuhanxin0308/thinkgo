package log

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
)

// RecordContext 在调用方上下文边界内把日志加入现有批量队列。
// 默认 OverflowSync 对旧 API 仍保持同步兜底兼容；本方法在队列饱和时只做
// 有界背压，截止后显式返回并计数，避免请求 goroutine 退化为逐条驱动 I/O。
func (l *Log) RecordContext(
	ctx context.Context,
	msg string,
	level string,
	fields map[string]interface{},
) error {
	if ctx == nil {
		return ErrInvalidLogContext
	}
	if l == nil {
		return ErrLogClosed
	}
	if !l.IsLevelEnabled(level) {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		atomic.AddUint64(&l.droppedEntries, 1)
		return errors.Join(ErrLogEntryDropped, contextErr)
	}

	entry := newLogEntry(l.now(), level, msg, fields)
	if l.isCallerEnabled() {
		if _, file, line, ok := runtime.Caller(1); ok {
			entry.Caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		}
	}

	l.ensureAsyncStarted()
	asyncCh, closing, overflowPolicy, admitted := l.beginLogProducer()
	if !admitted || asyncCh == nil {
		return ErrLogClosed
	}
	defer l.producers.Done()
	select {
	case asyncCh <- entry:
		return nil
	default:
	}
	if overflowPolicy == OverflowDrop {
		atomic.AddUint64(&l.droppedEntries, 1)
		return ErrLogEntryDropped
	}
	select {
	case asyncCh <- entry:
		return nil
	case <-ctx.Done():
		atomic.AddUint64(&l.droppedEntries, 1)
		return errors.Join(ErrLogEntryDropped, ctx.Err())
	case <-closing:
		atomic.AddUint64(&l.droppedEntries, 1)
		return errors.Join(ErrLogEntryDropped, ErrLogClosed)
	}
}

// RecordContextNonBlocking 在不执行驱动 I/O 的前提下尝试入队；队列满时立即返回可观测拒绝。
func (l *Log) RecordContextNonBlocking(
	ctx context.Context,
	msg string,
	level string,
	fields map[string]interface{},
) error {
	if ctx == nil {
		return ErrInvalidLogContext
	}
	if l == nil {
		return ErrLogClosed
	}
	if !l.IsLevelEnabled(level) {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		atomic.AddUint64(&l.droppedEntries, 1)
		return errors.Join(ErrLogEntryDropped, contextErr)
	}

	entry := newLogEntry(l.now(), level, msg, fields)
	if l.isCallerEnabled() {
		if _, file, line, ok := runtime.Caller(1); ok {
			entry.Caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		}
	}

	l.ensureAsyncStarted()
	asyncCh, _, _, admitted := l.beginLogProducer()
	if !admitted || asyncCh == nil {
		return ErrLogClosed
	}
	defer l.producers.Done()
	select {
	case asyncCh <- entry:
		return nil
	default:
		atomic.AddUint64(&l.droppedEntries, 1)
		return ErrLogEntryDropped
	}
}

// beginLogProducer 在状态锁内完成生产者登记，保证 Close 只有在禁止新登记后
// 才会等待现有生产者并关闭 asyncCh；真正的队列等待发生在锁外。
func (l *Log) beginLogProducer() (
	asyncCh chan *LogEntry,
	closing <-chan struct{},
	overflowPolicy OverflowPolicy,
	admitted bool,
) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed || l.asyncCh == nil {
		return nil, nil, l.overflowPolicy, false
	}
	l.producers.Add(1)
	return l.asyncCh, l.closing, l.overflowPolicy, true
}

// Flush 等待调用前已入队的日志完成刷盘，并返回尚未消费的驱动错误。
func (l *Log) Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		return ErrLogClosed
	}
	l.ensureAsyncStarted()

	flushed := make(chan struct{}, 1)
	flushCh, closing, admitted := l.beginFlushProducer()
	if !admitted || flushCh == nil {
		return ErrLogClosed
	}
	defer l.producers.Done()
	select {
	case flushCh <- flushed:
	case <-ctx.Done():
		return ctx.Err()
	case <-closing:
		return ErrLogClosed
	}

	select {
	case <-flushed:
		return l.takeDriverErrors()
	case <-ctx.Done():
		return ctx.Err()
	case <-closing:
		return ErrLogClosed
	}
}

func (l *Log) beginFlushProducer() (
	flushCh chan chan struct{},
	closing <-chan struct{},
	admitted bool,
) {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	if l.closed || l.flushCh == nil {
		return nil, nil, false
	}
	l.producers.Add(1)
	return l.flushCh, l.closing, true
}

// Close 优雅关闭日志系统，等待在途写入并聚合全部刷盘、驱动和通道关闭错误。
func (l *Log) Close() error {
	return l.CloseContext(context.Background())
}

// CloseContext 启动一次可靠排空，并允许调用方按上下文停止等待。
// 上下文到期只结束当前等待；后台关停继续保存全部已接受条目，后续 Close
// 会等待同一任务真实结束。Go 无法强制终止不合作的旧日志驱动。
func (l *Log) CloseContext(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		return ErrInvalidLogCloseContext
	}
	l.shutdownOnce.Do(func() {
		channelRegistryMu.Lock()
		l.stateMu.Lock()
		l.closed = true
		l.closeDone = make(chan struct{})
		if l.closing == nil {
			l.closing = make(chan struct{})
		}
		close(l.closing)
		asyncCh := l.asyncCh
		asyncDone := l.asyncDone
		channels := make([]*Log, 0, len(l.channels))
		for _, channel := range l.channels {
			if channel != nil && channel != l {
				channels = append(channels, channel)
			}
		}
		done := l.closeDone
		l.stateMu.Unlock()
		channelRegistryMu.Unlock()
		go l.closeResources(done, asyncCh, asyncDone, channels)
	})
	l.stateMu.RLock()
	done := l.closeDone
	l.stateMu.RUnlock()
	if done == nil {
		return l.logCloseError()
	}
	select {
	case <-done:
		return l.logCloseError()
	case <-ctx.Done():
		select {
		case <-done:
			return l.logCloseError()
		default:
			return ctx.Err()
		}
	}
}

func (l *Log) closeResources(
	done chan struct{},
	asyncCh chan *LogEntry,
	asyncDone chan struct{},
	channels []*Log,
) {
	var closeErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("日志关停 panic: %v", recovered))
		}
		closeErr = errors.Join(closeErr, l.takeDriverErrors())
		l.stateMu.Lock()
		l.closeErr = closeErr
		l.stateMu.Unlock()
		close(done)
	}()

	// 关停信号会先唤醒所有锁外等待者；只有登记生产者全部退出后，
	// asyncCh 才能关闭，从结构上排除 send-on-closed 与状态锁死锁。
	l.producers.Wait()
	if asyncCh != nil {
		close(asyncCh)
	}
	if asyncDone != nil {
		<-asyncDone
	}
	l.inFlight.Wait()

	l.stateMu.RLock()
	drivers := append([]Driver(nil), l.drivers...)
	fallbackWriter := l.fallbackWriter
	l.stateMu.RUnlock()
	for _, driver := range drivers {
		if err := safeDriverClose(driver); err != nil {
			l.reportDriverError(driver, "close", err, fallbackWriter, 0)
		}
	}

	for _, channel := range channels {
		closeErr = errors.Join(closeErr, channel.Close())
	}
}

func (l *Log) logCloseError() error {
	l.stateMu.RLock()
	defer l.stateMu.RUnlock()
	return l.closeErr
}
