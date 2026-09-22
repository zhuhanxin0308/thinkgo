package log

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLogRecordContextBoundsSaturatedQueue 验证默认可靠队列饱和且驱动阻塞时，
// 请求可按上下文停止等待；该条拒绝会显式计数，已接受条目仍在关停时完整保存。
func TestLogRecordContextBoundsSaturatedQueue(t *testing.T) {
	driver := &blockingRecordingDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := NewLog(driver)
	if err := logger.SetBufferSize(1); err != nil {
		t.Fatalf("设置饱和测试缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置饱和测试刷新周期失败: %v", err)
	}
	var releaseOnce sync.Once
	releaseDriver := func() { releaseOnce.Do(func() { close(driver.release) }) }
	t.Cleanup(releaseDriver)

	logger.Info("正在阻塞驱动的日志")
	select {
	case <-driver.entered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("日志驱动未进入阻塞批量保存")
	}
	// bufferSize=1 时异步通道容量为 2；后台正在驱动 I/O，因此这两条
	// 会稳定占满通道而不会被消费。
	logger.Info("已接受队列日志一")
	logger.Info("已接受队列日志二")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := logger.RecordContext(ctx, "应被显式拒绝的日志", LevelInfo, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrLogEntryDropped) {
		t.Fatalf("饱和队列应返回 deadline 和可观测拒绝错误，实际为 %v", err)
	}
	if dropped := logger.DroppedEntryCount(); dropped != 1 {
		t.Fatalf("饱和拒绝计数应为 1，实际为 %d", dropped)
	}

	releaseDriver()
	if err = logger.Close(); err != nil {
		t.Fatalf("释放驱动后关闭日志器失败: %v", err)
	}
	if saved := driver.savedCount(); saved != 3 {
		t.Fatalf("关停必须保存全部三条已接受日志，实际为 %d", saved)
	}
}

// TestLogRecordContextNonBlockingDropsWithoutDriverIO 验证非阻塞入队在队列饱和时
// 不会调用同步驱动，也不会让请求线程等待驱动恢复。
func TestLogRecordContextNonBlockingDropsWithoutDriverIO(t *testing.T) {
	driver := &blockingRecordingDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := NewLog(driver)
	if err := logger.SetBufferSize(1); err != nil {
		t.Fatalf("设置非阻塞日志缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置非阻塞日志刷新周期失败: %v", err)
	}
	var releaseOnce sync.Once
	releaseDriver := func() { releaseOnce.Do(func() { close(driver.release) }) }
	t.Cleanup(releaseDriver)

	logger.Info("占用非阻塞日志驱动")
	select {
	case <-driver.entered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("非阻塞日志驱动未进入批量保存")
	}
	logger.Info("非阻塞队列日志一")
	logger.Info("非阻塞队列日志二")

	started := time.Now()
	err := logger.RecordContextNonBlocking(context.Background(), "立即拒绝的日志", LevelInfo, nil)
	if !errors.Is(err, ErrLogEntryDropped) {
		releaseDriver()
		t.Fatalf("饱和非阻塞入队应返回可观测拒绝: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 20*time.Millisecond {
		releaseDriver()
		t.Fatalf("非阻塞入队不应等待驱动，耗时 %s", elapsed)
	}
	if writes := driver.writeCount(); writes != 0 {
		releaseDriver()
		t.Fatalf("非阻塞入队不应调用同步驱动，实际为 %d", writes)
	}
	if dropped := logger.DroppedEntryCount(); dropped != 1 {
		releaseDriver()
		t.Fatalf("非阻塞拒绝计数应为 1，实际为 %d", dropped)
	}

	releaseDriver()
	if err := logger.Close(); err != nil {
		t.Fatalf("释放非阻塞日志驱动后关闭失败: %v", err)
	}
	if saved := driver.savedCount(); saved != 3 {
		t.Fatalf("关闭时应保存全部三条已接受日志，实际为 %d", saved)
	}
}

// TestLogCloseContextUnblocksBackgroundQueueProducer 验证满队列上的 Background
// 入队不持有状态锁：并发关停可以先封闭准入并唤醒等待者，再安全关闭队列。
func TestLogCloseContextUnblocksBackgroundQueueProducer(t *testing.T) {
	driver := &blockingRecordingDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := NewLog(driver)
	if err := logger.SetBufferSize(1); err != nil {
		t.Fatalf("设置并发关停缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置并发关停刷新周期失败: %v", err)
	}
	var releaseOnce sync.Once
	releaseDriver := func() { releaseOnce.Do(func() { close(driver.release) }) }
	t.Cleanup(releaseDriver)

	logger.Info("正在阻塞关停驱动的日志")
	select {
	case <-driver.entered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("并发关停驱动未进入批量保存")
	}
	logger.Info("并发关停队列日志一")
	logger.Info("并发关停队列日志二")
	producerStarted := make(chan struct{})
	producerDone := make(chan error, 1)
	go func() {
		close(producerStarted)
		producerDone <- logger.RecordContext(context.Background(), "等待关停唤醒的日志", LevelInfo, nil)
	}()
	<-producerStarted
	select {
	case err := <-producerDone:
		releaseDriver()
		t.Fatalf("满队列 Background 入队应先等待，实际提前返回 %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := logger.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		releaseDriver()
		t.Fatalf("驱动仍阻塞时 CloseContext 应按截止返回，实际为 %v", err)
	}
	select {
	case err := <-producerDone:
		if !errors.Is(err, ErrLogClosed) || !errors.Is(err, ErrLogEntryDropped) {
			releaseDriver()
			t.Fatalf("关停应以可观测错误唤醒 Background 入队，实际为 %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		releaseDriver()
		t.Fatal("Background 入队持有状态锁，关停未能唤醒")
	}

	releaseDriver()
	if err := logger.Close(); err != nil {
		t.Fatalf("释放驱动后完整关停失败: %v", err)
	}
	if saved := driver.savedCount(); saved != 3 {
		t.Fatalf("并发关停必须保存三条已接受日志，实际为 %d", saved)
	}
}

// TestLogCloseContextBoundsBlockedDriverAndLaterDrains 验证关停等待可以按截止返回，
// 但后台排空不会被取消；驱动恢复后重复 Close 会等待真实完成且不丢已接受日志。
func TestLogCloseContextBoundsBlockedDriverAndLaterDrains(t *testing.T) {
	driver := &blockingRecordingDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := NewLog(driver)
	if err := logger.SetBufferSize(1); err != nil {
		t.Fatalf("设置关停测试缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置关停测试刷新周期失败: %v", err)
	}
	var releaseOnce sync.Once
	releaseDriver := func() { releaseOnce.Do(func() { close(driver.release) }) }
	t.Cleanup(releaseDriver)

	logger.Info("关停前已接受日志")
	select {
	case <-driver.entered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("日志驱动未进入关停阻塞点")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := logger.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		releaseDriver()
		t.Fatalf("阻塞驱动应让 CloseContext 按截止返回，实际为 %v", err)
	}
	if err := logger.RecordContext(context.Background(), "关闭后日志", LevelInfo, nil); !errors.Is(err, ErrLogClosed) {
		releaseDriver()
		t.Fatalf("关停开始后必须拒绝新日志，实际为 %v", err)
	}

	releaseDriver()
	if err := logger.Close(); err != nil {
		t.Fatalf("驱动恢复后完整关停失败: %v", err)
	}
	if saved := driver.savedCount(); saved != 1 {
		t.Fatalf("CloseContext 超时不能丢失已接受日志，实际保存 %d", saved)
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

// TestLogContextAPIsRejectNilWithoutClosing 验证 nil 上下文返回稳定错误，且失败的
// CloseContext 不会误触发关停，调用方修正上下文后仍可继续记录并正常排空。
func TestLogContextAPIsRejectNilWithoutClosing(t *testing.T) {
	logger := NewLog()
	var nilContext context.Context
	if err := logger.RecordContext(nilContext, "nil context", LevelInfo, nil); !errors.Is(err, ErrInvalidLogContext) {
		t.Fatalf("nil 入队上下文应返回稳定错误，实际为 %v", err)
	}
	if err := logger.CloseContext(nilContext); !errors.Is(err, ErrInvalidLogCloseContext) {
		t.Fatalf("nil 关停上下文应返回稳定错误，实际为 %v", err)
	}
	if err := logger.RecordContext(context.Background(), "仍可记录", LevelInfo, nil); err != nil {
		t.Fatalf("nil 关停上下文不应关闭日志器: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("有效关停上下文应正常完成: %v", err)
	}
}
