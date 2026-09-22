package framework

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingScopedCloseProbe struct {
	entered chan struct{}
	release chan struct{}
	closed  atomic.Int32
	once    sync.Once
}

func (probe *blockingScopedCloseProbe) Close() error {
	probe.closed.Add(1)
	probe.once.Do(func() { close(probe.entered) })
	<-probe.release
	return nil
}

type signalingScopedCloseProbe struct {
	closed chan struct{}
	count  atomic.Int32
	once   sync.Once
}

type panickingScopedCloseProbe struct{}

func (*panickingScopedCloseProbe) Close() error {
	panic("scoped closer panic")
}

func (probe *signalingScopedCloseProbe) Close() error {
	probe.count.Add(1)
	probe.once.Do(func() { close(probe.closed) })
	return nil
}

// TestContainerScopeCloseContextContinuesAfterBlockedCloser 验证 Scoped 服务严格按逆序关闭，
// 一个不合作的旧 Closer 不会阻止调用方按截止时间返回，但它真实结束前不得启动下一项。
func TestContainerScopeCloseContextContinuesAfterBlockedCloser(t *testing.T) {
	container := NewContainer()
	following := &signalingScopedCloseProbe{closed: make(chan struct{})}
	blocking := &blockingScopedCloseProbe{entered: make(chan struct{}), release: make(chan struct{})}
	container.BindScoped("close.following", func() interface{} { return following })
	container.BindScoped("close.blocking", func() interface{} { return blocking })
	scope := container.NewScope()
	if _, err := scope.Make("close.following"); err != nil {
		t.Fatalf("构建后续 Scoped 服务失败: %v", err)
	}
	if _, err := scope.Make("close.blocking"); err != nil {
		t.Fatalf("构建阻塞 Scoped 服务失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := scope.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		close(blocking.release)
		t.Fatalf("有界关闭应返回 DeadlineExceeded，实际为 %v", err)
	}
	select {
	case <-blocking.entered:
	default:
		close(blocking.release)
		t.Fatal("逆序首个阻塞 Closer 未启动")
	}
	select {
	case <-following.closed:
		close(blocking.release)
		t.Fatal("阻塞 Closer 真实返回前不得关闭后续 Scoped 服务")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := scope.Make("close.following"); !errors.Is(err, ErrContainerScopeClosed) {
		close(blocking.release)
		t.Fatalf("关闭开始后作用域必须拒绝解析，实际为 %v", err)
	}

	close(blocking.release)
	select {
	case <-following.closed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("阻塞 Closer 返回后未继续关闭后续 Scoped 服务")
	}
	if err := scope.Close(); err != nil {
		t.Fatalf("释放阻塞资源后完整关闭失败: %v", err)
	}
	if blocking.closed.Load() != 1 || following.count.Load() != 1 {
		t.Fatalf("Scoped 服务必须各关闭一次: blocking=%d following=%d", blocking.closed.Load(), following.count.Load())
	}
}

// TestContainerScopeCloseContextRejectsNil 验证作用域有界关闭不接受 nil 上下文。
func TestContainerScopeCloseContextRejectsNil(t *testing.T) {
	scope := NewContainer().NewScope()
	var nilContext context.Context
	if err := scope.CloseContext(nilContext); !errors.Is(err, ErrInvalidContainerScopeContext) {
		t.Fatalf("nil 关闭上下文应返回稳定错误，实际为 %v", err)
	}
	if err := scope.Close(); err != nil {
		t.Fatalf("nil 上下文失败后仍应允许正常关闭: %v", err)
	}
}

// TestCanceledContainerCloseRejectsResolutionBeforeReturn 验证调用前已取消时
// 作用域仍会先同步进入 closing，不能在返回取消错误后短暂解析新服务。
func TestCanceledContainerCloseRejectsResolutionBeforeReturn(t *testing.T) {
	container := NewContainer()
	blocking := &blockingScopedCloseProbe{entered: make(chan struct{}), release: make(chan struct{})}
	container.BindScoped("canceled.blocking", func() interface{} { return blocking })
	scope := container.NewScope()
	if _, err := scope.Make("canceled.blocking"); err != nil {
		t.Fatalf("构建已取消关闭服务失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scope.CloseContext(ctx); !errors.Is(err, context.Canceled) {
		close(blocking.release)
		t.Fatalf("已取消作用域关闭应返回 Canceled，实际为 %v", err)
	}
	if _, err := scope.Make("canceled.blocking"); !errors.Is(err, ErrContainerScopeClosed) {
		close(blocking.release)
		t.Fatalf("已取消关闭返回前必须拒绝作用域解析，实际为 %v", err)
	}
	close(blocking.release)
	if err := scope.Close(); err != nil {
		t.Fatalf("后台作用域关闭最终完成失败: %v", err)
	}
}

// TestContainerScopeCloseConvertsCloserPanicAndContinues 验证 Scoped Closer panic
// 不会击穿后台关闭协程或悬挂 closeDone，后续资源仍会被关闭。
func TestContainerScopeCloseConvertsCloserPanicAndContinues(t *testing.T) {
	container := NewContainer()
	following := &signalingScopedCloseProbe{closed: make(chan struct{})}
	panicking := &panickingScopedCloseProbe{}
	container.BindScoped("panic.following", func() interface{} { return following })
	container.BindScoped("panic.current", func() interface{} { return panicking })
	scope := container.NewScope()
	if _, err := scope.Make("panic.following"); err != nil {
		t.Fatalf("构建 panic 后续服务失败: %v", err)
	}
	if _, err := scope.Make("panic.current"); err != nil {
		t.Fatalf("构建 panic 服务失败: %v", err)
	}
	closeErr := scope.Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "scoped closer panic") {
		t.Fatalf("Scoped Closer panic 应转换为关闭错误，实际为 %v", closeErr)
	}
	select {
	case <-following.closed:
	default:
		t.Fatal("Scoped Closer panic 后续资源未继续关闭")
	}
	if repeated := scope.Close(); repeated == nil || repeated.Error() != closeErr.Error() {
		t.Fatalf("重复 Close 应返回稳定结果: first=%v repeated=%v", closeErr, repeated)
	}
}
