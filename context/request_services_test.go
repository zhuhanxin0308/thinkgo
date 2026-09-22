package context

import (
	stdcontext "context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type requestServiceScopeProbe struct {
	values  map[string]interface{}
	closed  atomic.Int32
	start   chan struct{}
	finish  chan struct{}
	name    string
	order   *[]string
	orderMu *sync.Mutex
}

type panickingRequestServiceScope struct{}

type panickingIdleRequestServiceScope struct {
	closed atomic.Int32
}

func (*panickingRequestServiceScope) Make(string, ...interface{}) (interface{}, error) {
	return nil, errors.New("service missing")
}

func (*panickingRequestServiceScope) Close() error {
	panic("request closer panic")
}

func (*panickingIdleRequestServiceScope) Make(string, ...interface{}) (interface{}, error) {
	return nil, errors.New("service missing")
}

func (scope *panickingIdleRequestServiceScope) Close() error {
	scope.closed.Add(1)
	return nil
}

func (*panickingIdleRequestServiceScope) CloseIfIdle() (bool, error) {
	panic("idle cleanup panic")
}

func (probe *requestServiceScopeProbe) Make(abstract string, _ ...interface{}) (interface{}, error) {
	value, exists := probe.values[abstract]
	if !exists {
		return nil, errors.New("service missing")
	}
	return value, nil
}

func (probe *requestServiceScopeProbe) Close() error {
	probe.closed.Add(1)
	if probe.order != nil {
		if probe.orderMu != nil {
			probe.orderMu.Lock()
		}
		*probe.order = append(*probe.order, probe.name)
		if probe.orderMu != nil {
			probe.orderMu.Unlock()
		}
	}
	if probe.start != nil {
		close(probe.start)
	}
	if probe.finish != nil {
		<-probe.finish
	}
	return nil
}

// TestRequestServiceScopeRebindingPreservesAllClosers 验证目标应用可以替换
// 当前解析器，同时请求结束仍会逆序关闭新旧两个作用域。
func TestRequestServiceScopeRebindingPreservesAllClosers(t *testing.T) {
	closeOrder := make([]string, 0, 2)
	caller := &requestServiceScopeProbe{
		name:   "caller",
		order:  &closeOrder,
		values: map[string]interface{}{"service": "caller"},
	}
	target := &requestServiceScopeProbe{
		name:   "target",
		order:  &closeOrder,
		values: map[string]interface{}{"service": "target"},
	}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(caller, caller),
	)
	if err != nil {
		t.Fatalf("创建调用方作用域请求失败: %v", err)
	}
	if err := WithServiceScope(target, target)(request); err != nil {
		t.Fatalf("重新绑定目标作用域失败: %v", err)
	}
	value, err := request.Make("service")
	if err != nil || value != "target" {
		t.Fatalf("重绑定后未使用目标解析器: value=%v err=%v", value, err)
	}
	if err := request.Cleanup(); err != nil {
		t.Fatalf("清理重绑定请求失败: %v", err)
	}
	if caller.closed.Load() != 1 || target.closed.Load() != 1 {
		t.Fatalf("新旧作用域必须各关闭一次: caller=%d target=%d", caller.closed.Load(), target.closed.Load())
	}
	if len(closeOrder) != 2 || closeOrder[0] != "target" || closeOrder[1] != "caller" {
		t.Fatalf("作用域关闭顺序错误: %#v", closeOrder)
	}
}

// TestRequestServiceScopeContract 验证请求只通过绑定的作用域解析服务，并拒绝类型化空指针。
func TestRequestServiceScopeContract(t *testing.T) {
	request, err := NewRequest(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if _, makeErr := request.Make("service"); !errors.Is(makeErr, ErrRequestServiceScopeUnavailable) {
		t.Fatalf("无作用域时必须返回稳定错误，实际为 %v", makeErr)
	}

	var nilProbe *requestServiceScopeProbe
	if _, optionErr := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(nilProbe, nilProbe),
	); !errors.Is(optionErr, ErrRequestServiceScopeUnavailable) {
		t.Fatalf("类型化空作用域必须被拒绝，实际为 %v", optionErr)
	}

	probe := &requestServiceScopeProbe{values: map[string]interface{}{"service": "ready"}}
	request, err = NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(probe, probe),
	)
	if err != nil {
		t.Fatalf("绑定请求作用域失败: %v", err)
	}
	value, err := request.Make("service")
	if err != nil || value != "ready" {
		t.Fatalf("请求作用域解析结果错误，value=%v err=%v", value, err)
	}
}

// TestRequestCleanupWaitsForServiceScope 验证并发清理会等待同一次作用域关闭完成，且不会重复关闭服务。
func TestRequestCleanupWaitsForServiceScope(t *testing.T) {
	probe := &requestServiceScopeProbe{
		values: map[string]interface{}{},
		start:  make(chan struct{}),
		finish: make(chan struct{}),
	}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(probe, probe),
	)
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}

	var wait sync.WaitGroup
	wait.Add(2)
	completed := make(chan struct{}, 2)
	for index := 0; index < 2; index++ {
		go func() {
			defer wait.Done()
			if cleanupErr := request.Cleanup(); cleanupErr != nil {
				t.Errorf("清理请求失败: %v", cleanupErr)
			}
			completed <- struct{}{}
		}()
	}
	<-probe.start
	select {
	case <-completed:
		t.Fatal("作用域关闭完成前 Cleanup 不应返回")
	default:
	}
	close(probe.finish)
	wait.Wait()
	if probe.closed.Load() != 1 {
		t.Fatalf("请求作用域必须只关闭一次，实际为 %d", probe.closed.Load())
	}
}

// TestRequestCleanupContextBoundsLegacyClosers 验证旧 io.Closer 即使不响应取消，
// 调用方也能按截止时间返回；后台监督任务必须等它真实结束后再按逆序关闭下一项。
func TestRequestCleanupContextBoundsLegacyClosers(t *testing.T) {
	closeOrder := make([]string, 0, 2)
	var closeOrderMu sync.Mutex
	following := &requestServiceScopeProbe{
		name:    "following",
		order:   &closeOrder,
		orderMu: &closeOrderMu,
		values:  map[string]interface{}{},
		start:   make(chan struct{}),
	}
	blocking := &requestServiceScopeProbe{
		name:    "blocking",
		order:   &closeOrder,
		orderMu: &closeOrderMu,
		values:  map[string]interface{}{},
		start:   make(chan struct{}),
		finish:  make(chan struct{}),
	}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(following, following),
	)
	if err != nil {
		t.Fatalf("创建有界清理请求失败: %v", err)
	}
	if err = WithServiceScope(blocking, blocking)(request); err != nil {
		t.Fatalf("绑定阻塞请求作用域失败: %v", err)
	}

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 30*time.Millisecond)
	defer cancel()
	if cleanupErr := request.CleanupContext(ctx); !errors.Is(cleanupErr, stdcontext.DeadlineExceeded) {
		close(blocking.finish)
		t.Fatalf("阻塞 Closer 应让有界清理返回 DeadlineExceeded，实际为 %v", cleanupErr)
	}
	select {
	case <-blocking.start:
	default:
		close(blocking.finish)
		t.Fatal("阻塞 Closer 未被启动")
	}
	select {
	case <-following.start:
		close(blocking.finish)
		t.Fatal("阻塞 Closer 真实返回前不得启动后续资源关闭")
	case <-time.After(100 * time.Millisecond):
	}
	if _, makeErr := request.Make("service"); !errors.Is(makeErr, ErrRequestServiceScopeUnavailable) {
		close(blocking.finish)
		t.Fatalf("清理开始后请求作用域必须立即停止解析服务，实际为 %v", makeErr)
	}

	close(blocking.finish)
	select {
	case <-following.start:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("阻塞 Closer 返回后未继续关闭后续资源")
	}
	if cleanupErr := request.Cleanup(); cleanupErr != nil {
		t.Fatalf("释放阻塞 Closer 后完整清理失败: %v", cleanupErr)
	}
	if blocking.closed.Load() != 1 || following.closed.Load() != 1 {
		t.Fatalf("每个请求作用域必须只关闭一次: blocking=%d following=%d", blocking.closed.Load(), following.closed.Load())
	}
	if len(closeOrder) != 2 || closeOrder[0] != "blocking" || closeOrder[1] != "following" {
		t.Fatalf("有界清理必须保持逆序启动: %#v", closeOrder)
	}
}

// TestRequestCleanupContextRejectsNil 验证显式有界清理不接受 nil 上下文。
func TestRequestCleanupContextRejectsNil(t *testing.T) {
	request, err := NewRequest(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatalf("创建 nil 上下文测试请求失败: %v", err)
	}
	var nilContext stdcontext.Context
	if cleanupErr := request.CleanupContext(nilContext); !errors.Is(cleanupErr, ErrInvalidRequestCleanupContext) {
		t.Fatalf("nil 清理上下文应返回稳定错误，实际为 %v", cleanupErr)
	}
}

// TestCanceledRequestCleanupDetachesScopeBeforeReturn 验证调用方在进入清理前
// 已经取消时，请求也会先同步封闭解析边界，再按取消结果结束当前等待。
func TestCanceledRequestCleanupDetachesScopeBeforeReturn(t *testing.T) {
	probe := &requestServiceScopeProbe{
		values: map[string]interface{}{"service": "value"},
		start:  make(chan struct{}),
		finish: make(chan struct{}),
	}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(probe, probe),
	)
	if err != nil {
		t.Fatalf("创建已取消清理请求失败: %v", err)
	}
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	cancel()
	if cleanupErr := request.CleanupContext(ctx); !errors.Is(cleanupErr, stdcontext.Canceled) {
		close(probe.finish)
		t.Fatalf("已取消清理应返回 Canceled，实际为 %v", cleanupErr)
	}
	if _, makeErr := request.Make("service"); !errors.Is(makeErr, ErrRequestServiceScopeUnavailable) {
		close(probe.finish)
		t.Fatalf("已取消清理返回前必须封闭请求作用域，实际为 %v", makeErr)
	}
	close(probe.finish)
	if cleanupErr := request.Cleanup(); cleanupErr != nil {
		t.Fatalf("后台请求清理最终完成失败: %v", cleanupErr)
	}
}

// TestRequestCleanupConvertsCloserPanicAndContinues 验证扩展 Closer 的 panic
// 会进入稳定错误并释放完成信号，不会阻止后续请求资源清理。
func TestRequestCleanupConvertsCloserPanicAndContinues(t *testing.T) {
	following := &requestServiceScopeProbe{
		values: map[string]interface{}{},
		start:  make(chan struct{}),
	}
	panicking := &panickingRequestServiceScope{}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(following, following),
	)
	if err != nil {
		t.Fatalf("创建 panic 清理请求失败: %v", err)
	}
	if err = WithServiceScope(panicking, panicking)(request); err != nil {
		t.Fatalf("绑定 panic Closer 失败: %v", err)
	}
	cleanupErr := request.Cleanup()
	if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "request closer panic") {
		t.Fatalf("Closer panic 应转换为清理错误，实际为 %v", cleanupErr)
	}
	select {
	case <-following.start:
	default:
		t.Fatal("Closer panic 后续资源未继续清理")
	}
	if repeated := request.Cleanup(); repeated == nil || repeated.Error() != cleanupErr.Error() {
		t.Fatalf("重复 Cleanup 应返回稳定结果: first=%v repeated=%v", cleanupErr, repeated)
	}
}

// TestRequestCleanupTaskConvertsInternalPanic 验证 multipart RemoveAll 等通用任务
// 即使内部发生 panic，也会向监督者交付错误而不是让 cleanupDone 永久悬挂。
func TestRequestCleanupTaskConvertsInternalPanic(t *testing.T) {
	taskErr := runRequestCleanupTask(func() error {
		panic("generic cleanup panic")
	})
	if taskErr == nil || !strings.Contains(taskErr.Error(), "generic cleanup panic") {
		t.Fatalf("通用清理 panic 应转换为稳定错误，实际为 %v", taskErr)
	}
}

// TestRequestIdleCleanupPanicFallsBackToSupervisedClose 验证零分配快速路径
// 本身发生 panic 时会回退到完整清理，同时保留故障信息和完成信号。
func TestRequestIdleCleanupPanicFallsBackToSupervisedClose(t *testing.T) {
	scope := &panickingIdleRequestServiceScope{}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithTrustedIdleServiceScope(scope, scope),
	)
	if err != nil {
		t.Fatalf("创建空闲清理 panic 请求失败: %v", err)
	}
	cleanupErr := request.Cleanup()
	if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "idle cleanup panic") {
		t.Fatalf("快速清理 panic 应进入稳定错误，实际为 %v", cleanupErr)
	}
	if scope.closed.Load() != 1 {
		t.Fatalf("快速清理 panic 后必须回退到一次完整关闭，实际为 %d", scope.closed.Load())
	}
	if repeated := request.Cleanup(); repeated == nil || repeated.Error() != cleanupErr.Error() {
		t.Fatalf("快速路径 panic 后重复 Cleanup 应稳定: first=%v repeated=%v", cleanupErr, repeated)
	}
}
