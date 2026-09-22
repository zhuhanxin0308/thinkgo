package context

import (
	stdcontext "context"
	"errors"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const requestCleanupBoundaryWait = 300 * time.Millisecond

type untrustedIdleServiceScope struct {
	idleEntered chan struct{}
	idleRelease chan struct{}
	closed      chan struct{}
	idleOnce    sync.Once
	closeOnce   sync.Once
}

func (*untrustedIdleServiceScope) Make(string, ...interface{}) (interface{}, error) {
	return nil, errors.New("service missing")
}

func (scope *untrustedIdleServiceScope) CloseIfIdle() (bool, error) {
	scope.idleOnce.Do(func() { close(scope.idleEntered) })
	<-scope.idleRelease
	return true, nil
}

func (scope *untrustedIdleServiceScope) Close() error {
	scope.closeOnce.Do(func() { close(scope.closed) })
	return nil
}

type blockingRequestServiceResolver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (resolver *blockingRequestServiceResolver) Make(string, ...interface{}) (interface{}, error) {
	resolver.once.Do(func() { close(resolver.entered) })
	<-resolver.release
	return "released", nil
}

func (*blockingRequestServiceResolver) Close() error { return nil }

type blockingMultipartBody struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (body *blockingMultipartBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.entered) })
	<-body.release
	return 0, io.EOF
}

func (*blockingMultipartBody) Close() error { return nil }

// TestRequestCleanupDoesNotInvokeUntrustedIdleExtension 验证普通扩展即使碰巧
// 暴露 CloseIfIdle，也不能在请求 goroutine 的清理快速路径执行任意代码。
func TestRequestCleanupDoesNotInvokeUntrustedIdleExtension(t *testing.T) {
	scope := &untrustedIdleServiceScope{
		idleEntered: make(chan struct{}),
		idleRelease: make(chan struct{}),
		closed:      make(chan struct{}),
	}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(scope, scope),
	)
	if err != nil {
		t.Fatalf("创建普通扩展作用域请求失败: %v", err)
	}

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- request.CleanupContext(ctx) }()
	select {
	case cleanupErr := <-done:
		if cleanupErr != nil {
			t.Fatalf("普通扩展清理失败: %v", cleanupErr)
		}
	case <-time.After(requestCleanupBoundaryWait):
		close(scope.idleRelease)
		<-done
		t.Fatal("普通扩展 CloseIfIdle 绕过了清理截止时间")
	}
	select {
	case <-scope.idleEntered:
		close(scope.idleRelease)
		t.Fatal("普通扩展的 CloseIfIdle 不应进入受信快速路径")
	default:
	}
	select {
	case <-scope.closed:
	default:
		t.Fatal("普通扩展必须通过受监督 Close 完成清理")
	}
}

// TestRequestCleanupBoundsBlockingResolverAdmission 验证已有 Make 即使持有读锁并
// 阻塞在外部解析器中，CleanupContext 仍按截止返回，且新的 Make 会立即被拒绝。
func TestRequestCleanupBoundsBlockingResolverAdmission(t *testing.T) {
	resolver := &blockingRequestServiceResolver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	request, err := NewRequest(
		httptest.NewRequest("GET", "/", nil),
		WithServiceScope(resolver, resolver),
	)
	if err != nil {
		t.Fatalf("创建阻塞解析器请求失败: %v", err)
	}
	makeDone := make(chan error, 1)
	go func() {
		_, makeErr := request.Make("blocked")
		makeDone <- makeErr
	}()
	select {
	case <-resolver.entered:
	case <-time.After(requestCleanupBoundaryWait):
		close(resolver.release)
		t.Fatal("阻塞解析器未进入 Make")
	}

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 30*time.Millisecond)
	defer cancel()
	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- request.CleanupContext(ctx) }()
	select {
	case cleanupErr := <-cleanupDone:
		if !errors.Is(cleanupErr, stdcontext.DeadlineExceeded) {
			close(resolver.release)
			t.Fatalf("阻塞 Make 应让当前等待按截止返回，实际为 %v", cleanupErr)
		}
	case <-time.After(requestCleanupBoundaryWait):
		close(resolver.release)
		<-cleanupDone
		t.Fatal("CleanupContext 同步等待了阻塞 Make 持有的 serviceMu")
	}
	if _, makeErr := request.Make("new"); !errors.Is(makeErr, ErrRequestServiceScopeUnavailable) {
		close(resolver.release)
		t.Fatalf("清理入口封闭后新的 Make 必须立即失败，实际为 %v", makeErr)
	}
	close(resolver.release)
	if makeErr := <-makeDone; makeErr != nil {
		t.Fatalf("清理前已进入的 Make 释放后失败: %v", makeErr)
	}
	if cleanupErr := request.Cleanup(); cleanupErr != nil {
		t.Fatalf("阻塞 Make 释放后完整清理失败: %v", cleanupErr)
	}
}

// TestRequestCleanupBoundsBlockingMultipartParse 验证 multipart 解析占用 formMu
// 时，清理入口不会同步等待正文读取；释放读取后后台清理仍会真实完成。
func TestRequestCleanupBoundsBlockingMultipartParse(t *testing.T) {
	body := &blockingMultipartBody{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	raw := httptest.NewRequest("POST", "/upload", body)
	raw.Header.Set("Content-Type", "multipart/form-data; boundary=request-cleanup-boundary")
	request, err := NewRequest(raw)
	if err != nil {
		t.Fatalf("创建阻塞 multipart 请求失败: %v", err)
	}
	parseDone := make(chan error, 1)
	go func() {
		_, fileErr := request.File("upload")
		parseDone <- fileErr
	}()
	select {
	case <-body.entered:
	case <-time.After(requestCleanupBoundaryWait):
		close(body.release)
		t.Fatal("multipart 正文读取未启动")
	}

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 30*time.Millisecond)
	defer cancel()
	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- request.CleanupContext(ctx) }()
	select {
	case cleanupErr := <-cleanupDone:
		if !errors.Is(cleanupErr, stdcontext.DeadlineExceeded) {
			close(body.release)
			t.Fatalf("阻塞 multipart 解析应让当前等待按截止返回，实际为 %v", cleanupErr)
		}
	case <-time.After(requestCleanupBoundaryWait):
		close(body.release)
		<-cleanupDone
		t.Fatal("CleanupContext 同步等待了 multipart 解析持有的 formMu")
	}
	close(body.release)
	if parseErr := <-parseDone; parseErr == nil {
		t.Fatal("不完整 multipart 正文应返回解析错误")
	}
	if cleanupErr := request.Cleanup(); cleanupErr != nil {
		t.Fatalf("multipart 解析释放后完整清理失败: %v", cleanupErr)
	}
}
