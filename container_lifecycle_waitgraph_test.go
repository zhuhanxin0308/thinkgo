package framework

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestContainerInjectedSingletonLazyResolution 验证单例构建完成后，其保存的注入容器
// 在运行期延迟解析回指自身的 Factory 或 Singleton 时不会误报循环依赖，也不会滞留构建期过期上下文或请求作用域。
func TestContainerInjectedSingletonLazyResolution(t *testing.T) {
	type contextKey struct{}
	type dispatcher struct {
		container *Container
		initTag   any
	}

	container := NewContainer()
	container.BindScoped("req_only", func() interface{} { return &struct{ _ int }{} })
	container.Bind("dispatcher", func(injected *Container) (*dispatcher, error) {
		return &dispatcher{
			container: injected,
			initTag:   injected.Context().Value(contextKey{}),
		}, nil
	})
	container.BindFactory("job", func(injected *Container) (string, error) {
		resolved, err := injected.Make("dispatcher")
		if err != nil {
			return "", err
		}
		if resolved == nil {
			return "", errors.New("dispatcher 为空")
		}
		return "ok", nil
	})

	reqScope := container.NewScope()
	initCtx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "boot-ctx"))
	raw, err := reqScope.MakeContext(initCtx, "dispatcher")
	if err != nil {
		t.Fatalf("首次构建 dispatcher 失败: %v", err)
	}
	cancel()
	if err := reqScope.Close(); err != nil {
		t.Fatalf("关闭触发构建的请求作用域失败: %v", err)
	}

	d := raw.(*dispatcher)
	if d.initTag != "boot-ctx" {
		t.Fatalf("构建期内应可见传入上下文，实际为 %v", d.initTag)
	}
	if d.container.Context() != context.Background() {
		t.Fatalf("单例构建结束后注入容器不应继续滞留构建期上下文，实际为 %v", d.container.Context())
	}

	result, err := d.container.Make("job")
	if err != nil || result != "ok" {
		t.Fatalf("运行期通过注入容器延迟解析回指自身的 Factory 失败: result=%v err=%v", result, err)
	}
	if _, err := d.container.Make("req_only"); !errors.Is(err, ErrScopedDependencyCaptured) {
		t.Fatalf("已完成单例在运行期通过保留容器解析 Scoped 服务仍必须返回 ErrScopedDependencyCaptured，实际为 %v", err)
	}
}

// TestContainerNestedSingletonRetainsOuterCycleGuardUntilOuterCompletes 验证内层单例先完成时，
// 外层单例尚未返回的同步窗口内仍能通过内层保留容器捕获对外层的循环依赖而不发生自锁。
func TestContainerNestedSingletonRetainsOuterCycleGuardUntilOuterCompletes(t *testing.T) {
	type innerService struct{ container *Container }
	container := NewContainer()
	container.Bind("inner", func(injected *Container) *innerService {
		return &innerService{container: injected}
	})
	var syncCycleErr error
	container.Bind("outer", func(injected *Container) (string, error) {
		raw, err := injected.Make("inner")
		if err != nil {
			return "", err
		}
		in := raw.(*innerService)
		_, syncCycleErr = in.container.Make("outer")
		return "ready", nil
	})

	val, err := container.Make("outer")
	if err != nil || val != "ready" {
		t.Fatalf("构建 outer 失败: val=%v err=%v", val, err)
	}
	if syncCycleErr == nil || !strings.Contains(syncCycleErr.Error(), "circular singleton dependency detected: outer -> outer") {
		t.Fatalf("外层构建未完成前通过内层容器回指外层应立即报循环依赖，实际为 %v", syncCycleErr)
	}

	rawInner, err := container.Make("inner")
	if err != nil {
		t.Fatalf("读取 inner 失败: %v", err)
	}
	resolvedOuter, err := rawInner.(*innerService).container.Make("outer")
	if err != nil || resolvedOuter != "ready" {
		t.Fatalf("外层构建完成后通过内层容器解析 outer 应正常返回，实际 val=%v err=%v", resolvedOuter, err)
	}
}

// TestContainerScopedInjectedLazyResolution 验证 Scoped 服务完成后保留容器可在同一作用域内延迟解析回指自身。
func TestContainerScopedInjectedLazyResolution(t *testing.T) {
	type sessionService struct{ container *Container }
	container := NewContainer()
	container.BindScoped("session_svc", func(injected *Container) *sessionService {
		return &sessionService{container: injected}
	})
	container.BindFactory("action_helper", func(injected *Container) (*sessionService, error) {
		raw, err := injected.Make("session_svc")
		if err != nil {
			return nil, err
		}
		return raw.(*sessionService), nil
	})

	scope := container.NewScope()
	defer func() { _ = scope.Close() }()

	raw, err := scope.Make("session_svc")
	if err != nil {
		t.Fatalf("解析 session_svc 失败: %v", err)
	}
	svc := raw.(*sessionService)
	helperResolved, err := svc.container.Make("action_helper")
	if err != nil || helperResolved != svc {
		t.Fatalf("同一作用域内延迟解析回指自身失败: got=%v want=%v err=%v", helperResolved, svc, err)
	}
}

// TestContainerCrossGoroutineNestedSingletonCycleDetected 验证跨 goroutine 嵌套构建单例时等待图完整记录祖先持有边，不发生死锁。
func TestContainerCrossGoroutineNestedSingletonCycleDetected(t *testing.T) {
	container := NewContainer()
	zEntered := make(chan struct{})
	yEntered := make(chan struct{})
	proceed := make(chan struct{})

	container.Bind("X", func(injected *Container) (interface{}, error) {
		return injected.Make("Z")
	})
	container.Bind("Z", func(injected *Container) (interface{}, error) {
		close(zEntered)
		<-proceed
		return injected.Make("Y")
	})
	container.Bind("Y", func(injected *Container) (interface{}, error) {
		close(yEntered)
		<-proceed
		return injected.Make("X")
	})

	errs := make(chan error, 2)
	go func() {
		_, err := container.Make("X")
		errs <- err
	}()
	go func() {
		_, err := container.Make("Y")
		errs <- err
	}()

	<-zEntered
	<-yEntered
	close(proceed)

	timeout := time.After(2 * time.Second)
	for range 2 {
		select {
		case err := <-errs:
			if err == nil || !strings.Contains(err.Error(), "circular singleton dependency detected") {
				t.Fatalf("跨协程嵌套单例环应返回循环依赖错误，实际为 %v", err)
			}
		case <-timeout:
			t.Fatal("跨协程嵌套单例构建发生死锁")
		}
	}
}

// TestContainerCrossGoroutineFactoryBridgeSingletonCycleDetected 验证经由 Factory 中转的跨 goroutine 单例循环不会因空节点丢边死锁。
func TestContainerCrossGoroutineFactoryBridgeSingletonCycleDetected(t *testing.T) {
	container := NewContainer()
	xEntered := make(chan struct{})
	yEntered := make(chan struct{})
	proceed := make(chan struct{})

	container.BindFactory("bridgeToY", func(injected *Container) (interface{}, error) {
		return injected.Make("Y")
	})
	container.BindFactory("bridgeToX", func(injected *Container) (interface{}, error) {
		return injected.Make("X")
	})
	container.Bind("X", func(injected *Container) (interface{}, error) {
		close(xEntered)
		<-proceed
		return injected.Make("bridgeToY")
	})
	container.Bind("Y", func(injected *Container) (interface{}, error) {
		close(yEntered)
		<-proceed
		return injected.Make("bridgeToX")
	})

	errs := make(chan error, 2)
	go func() {
		_, err := container.Make("X")
		errs <- err
	}()
	go func() {
		_, err := container.Make("Y")
		errs <- err
	}()

	<-xEntered
	<-yEntered
	close(proceed)

	timeout := time.After(2 * time.Second)
	for range 2 {
		select {
		case err := <-errs:
			if err == nil || !strings.Contains(err.Error(), "circular singleton dependency detected") {
				t.Fatalf("经由 Factory 中转的跨协程单例环应返回循环依赖错误，实际为 %v", err)
			}
		case <-timeout:
			t.Fatal("经由 Factory 中转的跨协程单例构建发生死锁")
		}
	}
}

// TestContainerScopeCrossGoroutineNestedAndFactoryCycleDetected 验证同一 ContainerScope 并发构建也具备完整多节点等待图检测。
func TestContainerScopeCrossGoroutineNestedAndFactoryCycleDetected(t *testing.T) {
	container := NewContainer()
	s2Entered := make(chan struct{})
	s3Entered := make(chan struct{})
	proceed := make(chan struct{})

	container.BindScoped("S1", func(injected *Container) (interface{}, error) {
		return injected.Make("S2")
	})
	container.BindScoped("S2", func(injected *Container) (interface{}, error) {
		close(s2Entered)
		<-proceed
		return injected.Make("bridgeToS3")
	})
	container.BindFactory("bridgeToS3", func(injected *Container) (interface{}, error) {
		return injected.Make("S3")
	})
	container.BindScoped("S3", func(injected *Container) (interface{}, error) {
		close(s3Entered)
		<-proceed
		return injected.Make("S1")
	})

	scope := container.NewScope()
	defer func() { _ = scope.Close() }()

	errs := make(chan error, 2)
	go func() {
		_, err := scope.Make("S1")
		errs <- err
	}()
	go func() {
		_, err := scope.Make("S3")
		errs <- err
	}()

	<-s2Entered
	<-s3Entered
	close(proceed)

	timeout := time.After(2 * time.Second)
	for range 2 {
		select {
		case err := <-errs:
			if err == nil || !strings.Contains(err.Error(), "circular scoped dependency detected") {
				t.Fatalf("跨协程作用域服务环应返回循环依赖错误，实际为 %v", err)
			}
		case <-timeout:
			t.Fatal("跨协程作用域服务构建发生死锁")
		}
	}
}
