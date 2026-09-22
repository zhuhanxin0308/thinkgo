package http

import (
	stdcontext "context"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
	frameworklog "github.com/zhuhanxin0308/thinkgo/framework/log"
	logdriver "github.com/zhuhanxin0308/thinkgo/framework/log/driver"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
	fwtelemetry "github.com/zhuhanxin0308/thinkgo/framework/telemetry"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	requestEndBoundaryTestTimeout = 40 * time.Millisecond
	requestEndBoundaryTestWait    = 2 * time.Second
)

type blockingHTTPEndListener struct {
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (listener *blockingHTTPEndListener) Handle(event.Event) error {
	listener.once.Do(func() { close(listener.entered) })
	<-listener.release
	if listener.finished != nil {
		close(listener.finished)
	}
	return nil
}

type blockingRequestScopedCloser struct {
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (closer *blockingRequestScopedCloser) Close() error {
	closer.once.Do(func() { close(closer.entered) })
	<-closer.release
	if closer.finished != nil {
		close(closer.finished)
	}
	return nil
}

type signalingRequestScopedCloser struct {
	closed chan struct{}
	once   sync.Once
}

func (closer *signalingRequestScopedCloser) Close() error {
	closer.once.Do(func() { close(closer.closed) })
	return nil
}

type blockingBatchLogDriver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	saved   int
}

func (driver *blockingBatchLogDriver) SaveEntries(entries []*frameworklog.LogEntry) error {
	driver.once.Do(func() { close(driver.entered) })
	<-driver.release
	driver.mu.Lock()
	driver.saved += len(entries)
	driver.mu.Unlock()
	return nil
}

func (*blockingBatchLogDriver) WriteEntry(*frameworklog.LogEntry) error { return nil }
func (*blockingBatchLogDriver) Close() error                            { return nil }

func (driver *blockingBatchLogDriver) savedCount() int {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.saved
}

type accessLogBatchDriver struct {
	mu         sync.Mutex
	batchSizes []int
	writes     int
}

type saturatedAccessLogDriver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	saved   int
	writes  int
}

func (driver *saturatedAccessLogDriver) SaveEntries(entries []*frameworklog.LogEntry) error {
	driver.once.Do(func() { close(driver.entered) })
	<-driver.release
	driver.mu.Lock()
	driver.saved += len(entries)
	driver.mu.Unlock()
	return nil
}

func (driver *saturatedAccessLogDriver) WriteEntry(*frameworklog.LogEntry) error {
	driver.mu.Lock()
	driver.writes++
	driver.mu.Unlock()
	<-driver.release
	return nil
}

func (*saturatedAccessLogDriver) Close() error { return nil }

func (driver *saturatedAccessLogDriver) snapshot() (saved int, writes int) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.saved, driver.writes
}

func (driver *accessLogBatchDriver) SaveEntries(entries []*frameworklog.LogEntry) error {
	driver.mu.Lock()
	driver.batchSizes = append(driver.batchSizes, len(entries))
	driver.mu.Unlock()
	return nil
}

func (driver *accessLogBatchDriver) WriteEntry(*frameworklog.LogEntry) error {
	driver.mu.Lock()
	driver.writes++
	driver.mu.Unlock()
	return nil
}

func (*accessLogBatchDriver) Close() error { return nil }

func (driver *accessLogBatchDriver) snapshot() ([]int, int) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return append([]int(nil), driver.batchSizes...), driver.writes
}

// TestServeHTTPBoundsPostResponseLifecycle 验证响应已经提交后，即使 HttpEnd、
// 旧 terminate 和 Scoped Closer 不响应取消，请求也会在收尾截止时间后返回；
// 后台监督任务仍须严格串行推进，指标和 Span 必须按调用方边界完成。
func TestServeHTTPBoundsPostResponseLifecycle(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := mustHTTPConfig(t, app).Set("app.server.request_end_timeout_ms", int(requestEndBoundaryTestTimeout.Milliseconds())); err != nil {
		t.Fatalf("设置请求收尾超时失败: %v", err)
	}

	endListener := &blockingHTTPEndListener{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	if err := app.Event().Listen(event.EventHttpEnd, endListener); err != nil {
		t.Fatalf("注册阻塞 HttpEnd 监听器失败: %v", err)
	}
	followingEndListener := make(chan struct{})
	if err := app.Event().Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(event.Event) error {
		close(followingEndListener)
		return nil
	}}); err != nil {
		t.Fatalf("注册后续 HttpEnd 监听器失败: %v", err)
	}

	contextTerminatorEntered := make(chan struct{})
	legacyTerminatorEntered := make(chan struct{})
	legacyTerminatorRelease := make(chan struct{})
	legacyTerminatorFinished := make(chan struct{})
	mustHTTPMiddleware(t, app).
		PipeLifecycleContext(
			func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				return next(request)
			},
			func(ctx stdcontext.Context, _ *fwcontext.Request, _ *fwcontext.Response) {
				if _, hasDeadline := ctx.Deadline(); !hasDeadline {
					return
				}
				close(contextTerminatorEntered)
				<-ctx.Done()
			},
		).
		PipeLifecycle(
			func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				return next(request)
			},
			func(*fwcontext.Request, *fwcontext.Response) {
				close(legacyTerminatorEntered)
				<-legacyTerminatorRelease
				close(legacyTerminatorFinished)
			},
		)

	followingCloser := &signalingRequestScopedCloser{closed: make(chan struct{})}
	blockingCloser := &blockingRequestScopedCloser{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	if err := app.BindScoped("request_end.following", func() interface{} { return followingCloser }); err != nil {
		t.Fatalf("注册后续 Scoped Closer 失败: %v", err)
	}
	if err := app.BindScoped("request_end.blocking", func() interface{} { return blockingCloser }); err != nil {
		t.Fatalf("注册阻塞 Scoped Closer 失败: %v", err)
	}
	capturedRequest := make(chan *fwcontext.Request, 1)
	if _, err := mustHTTPRoute(t, app).Get("/request-end-boundary", func(request *fwcontext.Request) *fwcontext.Response {
		capturedRequest <- request
		if _, makeErr := request.Make("request_end.following"); makeErr != nil {
			return fwcontext.NewResponse().Code(stdhttp.StatusInternalServerError).Content(makeErr.Error())
		}
		if _, makeErr := request.Make("request_end.blocking"); makeErr != nil {
			return fwcontext.NewResponse().Code(stdhttp.StatusInternalServerError).Content(makeErr.Error())
		}
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册请求收尾测试路由失败: %v", err)
	}

	registry := mustHTTPMetrics(t, app)
	registry.Enable()
	spanRecorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { _ = provider.Shutdown(stdcontext.Background()) })
	tracing, err := fwtelemetry.New(fwtelemetry.Config{
		Enabled:             true,
		Provider:            provider,
		Propagator:          propagation.TraceContext{},
		InstrumentationName: "thinkgo/request-end-boundary-test",
	})
	if err != nil {
		t.Fatalf("创建请求收尾测试追踪器失败: %v", err)
	}
	if err = app.Instance(string(framework.ServiceTelemetry), tracing); err != nil {
		t.Fatalf("替换请求收尾测试追踪器失败: %v", err)
	}

	var releaseEndOnce sync.Once
	var releaseTerminatorOnce sync.Once
	var releaseCloserOnce sync.Once
	releaseEnd := func() { releaseEndOnce.Do(func() { close(endListener.release) }) }
	releaseTerminator := func() { releaseTerminatorOnce.Do(func() { close(legacyTerminatorRelease) }) }
	releaseCloser := func() { releaseCloserOnce.Do(func() { close(blockingCloser.release) }) }
	releaseAll := func() {
		releaseEnd()
		releaseTerminator()
		releaseCloser()
	}
	t.Cleanup(releaseAll)

	handler := newTestHTTPHandler(t, app)
	requestContext, cancelRequest := stdcontext.WithCancel(stdcontext.Background())
	request := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/request-end-boundary", nil).WithContext(requestContext)
	cancelRequest()
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()

	waitHTTPBoundarySignal(t, endListener.entered, "HttpEnd 监听器未启动")
	select {
	case <-done:
	case <-time.After(requestEndBoundaryTestWait):
		releaseAll()
		<-done
		t.Fatal("请求收尾超过配置截止时间")
	}
	select {
	case <-followingEndListener:
		releaseAll()
		t.Fatal("阻塞 HttpEnd 监听器真实返回前不得启动后续 HttpEnd 监听器")
	case <-time.After(100 * time.Millisecond):
	}
	for stage, signal := range map[string]<-chan struct{}{
		"后续 HttpEnd 监听器":    followingEndListener,
		"ContextTerminator": contextTerminatorEntered,
		"旧 terminate":       legacyTerminatorEntered,
		"阻塞 Scoped Closer":  blockingCloser.entered,
		"后续 Scoped Closer":  followingCloser.closed,
	} {
		select {
		case <-signal:
			releaseAll()
			t.Fatalf("阻塞 HttpEnd 监听器真实返回前不得启动%s", stage)
		default:
		}
	}

	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("响应提交结果被收尾失败污染: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	snapshot := registry.Snapshot()
	if snapshot.InFlight != 0 || snapshot.Requests != 1 || snapshot.DurationCount != 1 {
		t.Fatalf("请求收尾超时后指标未完成: %#v", snapshot)
	}
	if ended := spanRecorder.Ended(); len(ended) != 1 {
		t.Fatalf("请求收尾超时后服务端 Span 未完成: %d", len(ended))
	}
	releaseEnd()
	waitHTTPBoundarySignal(t, endListener.finished, "HttpEnd 监听器释放后未完成")
	waitHTTPBoundarySignal(t, followingEndListener, "首个 HttpEnd 监听器返回后未继续分发")
	waitHTTPBoundarySignal(t, contextTerminatorEntered, "HttpEnd 全部返回后未启动 ContextTerminator")
	waitHTTPBoundarySignal(t, legacyTerminatorEntered, "ContextTerminator 返回后未启动旧 terminate")
	select {
	case <-blockingCloser.entered:
		releaseAll()
		t.Fatal("旧 terminate 真实返回前不得启动请求资源清理")
	default:
	}
	releaseTerminator()
	waitHTTPBoundarySignal(t, legacyTerminatorFinished, "旧 terminate 释放后未完成")
	waitHTTPBoundarySignal(t, blockingCloser.entered, "旧 terminate 返回后未启动阻塞 Scoped Closer")
	select {
	case <-followingCloser.closed:
		releaseAll()
		t.Fatal("阻塞 Scoped Closer 真实返回前不得关闭后续作用域资源")
	default:
	}
	releaseCloser()
	waitHTTPBoundarySignal(t, blockingCloser.finished, "Scoped Closer 释放后未完成")
	waitHTTPBoundarySignal(t, followingCloser.closed, "阻塞 Scoped Closer 返回后未继续清理后续资源")
	requestUnderTest := <-capturedRequest
	if cleanupErr := requestUnderTest.Cleanup(); cleanupErr != nil {
		t.Fatalf("释放阻塞扩展后请求清理未真实完成: %v", cleanupErr)
	}
}

// TestSaturatedAccessLogQueueStillBoundsServeHTTP 验证默认访问日志队列饱和时，
// 请求不会退化为同步 WriteEntry/fsync；截止后拒绝可观测，已接受日志仍可靠排空。
func TestSaturatedAccessLogQueueStillBoundsServeHTTP(t *testing.T) {
	driver := &saturatedAccessLogDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := frameworklog.NewLog(driver)
	if err := logger.SetBufferSize(1); err != nil {
		t.Fatalf("设置饱和访问日志缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置饱和访问日志刷新周期失败: %v", err)
	}
	var releaseOnce sync.Once
	releaseDriver := func() { releaseOnce.Do(func() { close(driver.release) }) }
	t.Cleanup(func() {
		releaseDriver()
		_ = logger.Close()
	})

	logger.Info("占用饱和访问日志驱动")
	flushDone := make(chan error, 1)
	go func() { flushDone <- logger.Flush(stdcontext.Background()) }()
	waitHTTPBoundarySignal(t, driver.entered, "饱和访问日志驱动未进入批量保存")
	logger.Info("已接受访问日志一")
	logger.Info("已接受访问日志二")
	initialDropped := logger.DroppedEntryCount()

	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	// 先释放阻塞驱动，再让应用清理日志服务，避免失败路径中的清理顺序互相等待。
	t.Cleanup(releaseDriver)
	if err := mustHTTPConfig(t, app).Set("app.server.request_end_timeout_ms", int(requestEndBoundaryTestTimeout.Milliseconds())); err != nil {
		t.Fatalf("设置饱和访问日志收尾超时失败: %v", err)
	}
	if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
		t.Fatalf("替换饱和访问日志服务失败: %v", err)
	}
	if _, err := mustHTTPRoute(t, app).Get("/saturated-access-log", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册饱和访问日志路由失败: %v", err)
	}

	handler := newTestHTTPHandler(t, app)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/saturated-access-log", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(requestEndBoundaryTestWait):
		releaseDriver()
		<-done
		<-flushDone
		t.Fatal("饱和访问日志退化为同步驱动 I/O，ServeHTTP 未按收尾截止返回")
	}
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("日志拒绝不应污染已提交响应: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if dropped := logger.DroppedEntryCount(); dropped != initialDropped+1 {
		t.Fatalf("非阻塞访问日志应产生一次可观测拒绝，初始为 %d，实际为 %d", initialDropped, dropped)
	}
	if _, writes := driver.snapshot(); writes != 0 {
		t.Fatalf("访问日志不应在请求线程调用同步 WriteEntry，实际为 %d", writes)
	}

	releaseDriver()
	if err := <-flushDone; err != nil {
		t.Fatalf("释放饱和访问日志驱动后首次 Flush 失败: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭饱和访问日志器失败: %v", err)
	}
	if saved, writes := driver.snapshot(); saved != 3 || writes != 0 {
		t.Fatalf("关停必须保存三条已接受日志且不做同步写: saved=%d writes=%d", saved, writes)
	}
}

// TestDirectEndUsesSameDeadlineForErrorLogging 验证直接 Http.End 在收尾失败后
// 记录错误时不能重新获得无限预算；队列饱和时应复用原截止并立即可观测拒绝。
func TestDirectEndUsesSameDeadlineForErrorLogging(t *testing.T) {
	driver := &saturatedAccessLogDriver{entered: make(chan struct{}), release: make(chan struct{})}
	logger := frameworklog.NewLog(driver)
	if err := logger.SetBufferSize(1); err != nil {
		t.Fatalf("设置直接 End 饱和缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置直接 End 刷新周期失败: %v", err)
	}
	endListener := &blockingHTTPEndListener{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseAll := func() {
		releaseOnce.Do(func() {
			close(driver.release)
			close(endListener.release)
		})
	}
	t.Cleanup(func() {
		releaseAll()
		_ = logger.Close()
	})

	logger.Info("占用直接 End 日志驱动")
	flushDone := make(chan error, 1)
	go func() { flushDone <- logger.Flush(stdcontext.Background()) }()
	waitHTTPBoundarySignal(t, driver.entered, "直接 End 日志驱动未进入批量保存")
	logger.Info("直接 End 队列日志一")
	logger.Info("直接 End 队列日志二")

	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	// 先释放阻塞驱动，再让应用清理日志服务，避免失败路径中的清理顺序互相等待。
	t.Cleanup(releaseAll)
	if err := mustHTTPConfig(t, app).Set("app.server.request_end_timeout_ms", int(requestEndBoundaryTestTimeout.Milliseconds())); err != nil {
		t.Fatalf("设置直接 End 收尾超时失败: %v", err)
	}
	if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
		t.Fatalf("替换直接 End 日志服务失败: %v", err)
	}
	if err := app.Event().Listen(event.EventHttpEnd, endListener); err != nil {
		t.Fatalf("注册直接 End 阻塞监听器失败: %v", err)
	}
	if _, err := mustHTTPRoute(t, app).Get("/direct-end", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册直接 End 路由失败: %v", err)
	}

	kernel := newTestHTTPHandler(t, app)
	raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/direct-end", nil)
	request, err := fwcontext.NewRequest(raw)
	if err != nil {
		t.Fatalf("创建直接 End 请求失败: %v", err)
	}
	response := kernel.Run(request)
	if response == nil {
		t.Fatal("直接 Run 未返回响应")
	}
	if err = response.Send(httptest.NewRecorder()); err != nil {
		t.Fatalf("提交直接 End 响应失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		kernel.End(response)
		close(done)
	}()
	waitHTTPBoundarySignal(t, endListener.entered, "直接 End 的 HttpEnd 监听器未启动")
	select {
	case <-done:
	case <-time.After(requestEndBoundaryTestWait):
		releaseAll()
		<-done
		<-flushDone
		t.Fatal("直接 End 的错误日志在原截止后同步阻塞")
	}
	if dropped := logger.DroppedEntryCount(); dropped != 1 {
		t.Fatalf("直接 End 饱和错误日志应产生一次可观测拒绝，实际为 %d", dropped)
	}
	if _, writes := driver.snapshot(); writes != 0 {
		t.Fatalf("直接 End 不应同步调用 WriteEntry，实际为 %d", writes)
	}

	releaseAll()
	if err = <-flushDone; err != nil {
		t.Fatalf("释放直接 End 日志驱动后 Flush 失败: %v", err)
	}
	if err = logger.Close(); err != nil {
		t.Fatalf("关闭直接 End 日志器失败: %v", err)
	}
}

// TestContextTerminatorReceivesActiveDeadline 验证新 ContextTerminator 在收尾开始时
// 能看到尚未到期的独立截止时间，并可主动响应取消而不遗留后台 goroutine。
func TestContextTerminatorReceivesActiveDeadline(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := mustHTTPConfig(t, app).Set("app.server.request_end_timeout_ms", int(requestEndBoundaryTestTimeout.Milliseconds())); err != nil {
		t.Fatalf("设置请求收尾超时失败: %v", err)
	}
	entered := make(chan struct{})
	finished := make(chan struct{})
	var activeDeadline atomic.Bool
	mustHTTPMiddleware(t, app).PipeLifecycleContext(
		func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			return next(request)
		},
		func(ctx stdcontext.Context, _ *fwcontext.Request, _ *fwcontext.Response) {
			_, hasDeadline := ctx.Deadline()
			activeDeadline.Store(hasDeadline && ctx.Err() == nil)
			close(entered)
			<-ctx.Done()
			close(finished)
		},
	)
	if _, err := mustHTTPRoute(t, app).Get("/context-terminate", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册 ContextTerminator 路由失败: %v", err)
	}

	handler := newTestHTTPHandler(t, app)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/context-terminate", nil))
	waitHTTPBoundarySignal(t, entered, "ContextTerminator 未启动")
	waitHTTPBoundarySignal(t, finished, "ContextTerminator 未响应截止时间")
	if !activeDeadline.Load() {
		t.Fatal("ContextTerminator 应收到尚未到期的独立收尾截止时间")
	}
}

// TestBlockingLogDriverDoesNotBlockRequestCompletion 验证日志驱动正在执行慢批量 I/O 时，
// HTTP 请求只负责可靠入队，不再通过逐请求 Flush 等待该驱动。
func TestBlockingLogDriverDoesNotBlockRequestCompletion(t *testing.T) {
	driver := &blockingBatchLogDriver{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseDriver := func() { releaseOnce.Do(func() { close(driver.release) }) }
	logger := frameworklog.NewLog(driver)
	if err := logger.SetBufferSize(16); err != nil {
		t.Fatalf("设置阻塞日志缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置阻塞日志刷新周期失败: %v", err)
	}
	logger.Info("占用日志驱动")
	flushDone := make(chan error, 1)
	go func() { flushDone <- logger.Flush(stdcontext.Background()) }()
	waitHTTPBoundarySignal(t, driver.entered, "阻塞日志驱动未进入批量保存")

	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	// 先释放阻塞驱动，再让应用清理日志服务，避免失败路径中的清理顺序互相等待。
	t.Cleanup(releaseDriver)
	if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
		releaseDriver()
		t.Fatalf("替换阻塞日志服务失败: %v", err)
	}
	if _, err := mustHTTPRoute(t, app).Get("/blocked-log", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		releaseDriver()
		t.Fatalf("注册阻塞日志路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/blocked-log", nil))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(requestEndBoundaryTestWait):
		releaseDriver()
		<-done
		<-flushDone
		t.Fatal("逐请求日志 Flush 仍会被慢驱动阻塞")
	}
	releaseDriver()
	if err := <-flushDone; err != nil {
		t.Fatalf("释放阻塞驱动后首次 Flush 失败: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭阻塞日志器失败: %v", err)
	}
	if recorder.Code != stdhttp.StatusOK || driver.savedCount() != 2 {
		t.Fatalf("请求日志未在关停时可靠刷盘: status=%d saved=%d", recorder.Code, driver.savedCount())
	}
}

// TestHTTPAccessLogsRemainBatchedUntilShutdown 验证默认异步访问日志不会按请求
// 触发批量保存，关停时则把所有已接受条目作为完整批次刷出。
func TestHTTPAccessLogsRemainBatchedUntilShutdown(t *testing.T) {
	driver := &accessLogBatchDriver{}
	logger := frameworklog.NewLog(driver)
	if err := logger.SetBufferSize(16); err != nil {
		t.Fatalf("设置访问日志缓冲区失败: %v", err)
	}
	if err := logger.SetFlushInterval(time.Hour); err != nil {
		t.Fatalf("设置访问日志刷新周期失败: %v", err)
	}
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
		t.Fatalf("替换访问日志服务失败: %v", err)
	}
	if _, err := mustHTTPRoute(t, app).Get("/batched-access-log", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册批量访问日志路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	for index := 0; index < 2; index++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/batched-access-log", nil))
		if recorder.Code != stdhttp.StatusOK {
			t.Fatalf("第 %d 个访问日志请求失败: %d", index+1, recorder.Code)
		}
	}
	if batches, writes := driver.snapshot(); len(batches) != 0 || writes != 0 {
		t.Fatalf("请求热路径不应同步保存访问日志: batches=%v writes=%d", batches, writes)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭访问日志器失败: %v", err)
	}
	batches, writes := driver.snapshot()
	if len(batches) != 1 || batches[0] != 2 || writes != 0 {
		t.Fatalf("关停应一次刷出两个访问日志: batches=%v writes=%d", batches, writes)
	}
}

// TestDefaultFileAccessLogWaitsForBatchFlush 验证实际文件驱动在默认异步路径上
// 不会逐请求创建和同步日志文件，关停屏障则会把完整访问日志可靠写出。
func TestDefaultFileAccessLogWaitsForBatchFlush(t *testing.T) {
	logDirectory := t.TempDir()
	fileDriver, err := logdriver.NewFileWithOptions(logDirectory, logdriver.FileOptions{})
	if err != nil {
		t.Fatalf("创建默认文件日志驱动失败: %v", err)
	}
	logger := frameworklog.NewLog(fileDriver)
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if err = app.Instance(string(framework.ServiceLog), logger); err != nil {
		t.Fatalf("替换文件日志服务失败: %v", err)
	}
	if _, err = mustHTTPRoute(t, app).Get("/file-access-log", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	}); err != nil {
		t.Fatalf("注册文件访问日志路由失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	for index := 0; index < 2; index++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/file-access-log", nil))
		if recorder.Code != stdhttp.StatusOK {
			t.Fatalf("第 %d 个文件访问日志请求失败: %d", index+1, recorder.Code)
		}
	}
	entries, err := os.ReadDir(logDirectory)
	if err != nil {
		t.Fatalf("读取文件日志目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("逐请求路径不应提前写出文件日志，实际文件数为 %d", len(entries))
	}
	if err = logger.Close(); err != nil {
		t.Fatalf("关闭文件日志器失败: %v", err)
	}
	entries, err = os.ReadDir(logDirectory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("关停后应生成一个批量日志文件: files=%d err=%v", len(entries), err)
	}
	content, err := os.ReadFile(filepath.Join(logDirectory, entries[0].Name()))
	if err != nil {
		t.Fatalf("读取关停后的访问日志失败: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(content)), "\n") + 1; lines != 2 {
		t.Fatalf("关停应写出两条完整访问日志，实际为 %d 条: %q", lines, string(content))
	}
}

func waitHTTPBoundarySignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(requestEndBoundaryTestWait):
		t.Fatal(message)
	}
}

var _ middleware.ContextTerminator
