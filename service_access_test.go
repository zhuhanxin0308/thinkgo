package framework

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
)

// TestResolveServiceUsesStableServiceNames 验证应用通过公开服务名称解析已装配服务。
func TestResolveServiceUsesStableServiceNames(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	tests := []struct {
		name     ServiceName
		expected interface{}
	}{
		{name: ServiceApp, expected: app},
		{name: ServiceContainer, expected: app.container},
		{name: ServiceConfig, expected: app.config},
		{name: ServiceEnv, expected: app.env},
		{name: ServiceRoute, expected: app.route},
		{name: ServiceMiddleware, expected: app.middleware},
	}
	for _, test := range tests {
		actual, err := app.ResolveService(test.name)
		if err != nil {
			t.Fatalf("解析基础服务 %q 失败: %v", test.name, err)
		}
		if actual != test.expected {
			t.Fatalf("服务 %q 未返回容器中的同一实例: expected=%p actual=%p", test.name, test.expected, actual)
		}
	}

	if _, err := app.ResolveService(ServiceCache); !errors.Is(err, ErrServiceNotReady) {
		t.Fatalf("未初始化应用解析运行时服务应返回就绪状态错误，实际为 %v", err)
	}
}

// TestResolveServiceAsChecksServiceType 验证类型安全解析不会静默接受错误的服务类型。
func TestResolveServiceAsChecksServiceType(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app, err := BuildApp(basePath)
	if err != nil {
		t.Fatalf("构建测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	cacheService, err := ResolveServiceAs[*cache.Cache](app, ServiceCache)
	if err != nil {
		t.Fatalf("类型安全解析缓存服务失败: %v", err)
	}
	if cacheService != app.cache {
		t.Fatalf("类型安全解析结果与应用字段不一致: expected=%p actual=%p", app.cache, cacheService)
	}

	if _, err := ResolveServiceAs[*cache.Cache](app, ServiceConfig); !errors.Is(err, ErrServiceTypeMismatch) {
		t.Fatalf("错误服务类型应返回类型不匹配错误，实际为 %v", err)
	}
}

// TestResolveServiceRejectsInvalidState 验证 nil 应用、非法服务名和关闭状态不会继续访问容器。
func TestResolveServiceRejectsInvalidState(t *testing.T) {
	var nilApp *App
	if _, err := nilApp.ResolveService(ServiceConfig); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil 应用应返回空应用错误，实际为 %v", err)
	}

	app := NewAppUninitialized(t.TempDir())
	if _, err := app.ResolveService(ServiceName(" ")); !errors.Is(err, ErrInvalidServiceName) {
		t.Fatalf("非法服务名应返回服务名错误，实际为 %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭测试应用失败: %v", err)
	}
	if _, err := app.ResolveService(ServiceConfig); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭应用解析服务应返回应用已关闭错误，实际为 %v", err)
	}
}

type typedServiceProbe struct {
	sequence int
}

// TestTypedServiceBindingLifecycle 验证泛型服务键同时约束名称、工厂返回类型和作用域解析类型。
func TestTypedServiceBindingLifecycle(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	key, err := NewTypedServiceKey[*typedServiceProbe]("request.probe")
	if err != nil {
		t.Fatalf("创建泛型服务键失败: %v", err)
	}
	builds := 0
	if err = BindScopedTyped(app, key, func(_ *Container) (*typedServiceProbe, error) {
		builds++
		return &typedServiceProbe{sequence: builds}, nil
	}); err != nil {
		t.Fatalf("绑定泛型 Scoped 服务失败: %v", err)
	}

	firstScope, err := app.NewScope()
	if err != nil {
		t.Fatalf("创建第一个作用域失败: %v", err)
	}
	defer firstScope.Close()
	first, err := ResolveTyped(firstScope, key)
	if err != nil {
		t.Fatalf("解析泛型 Scoped 服务失败: %v", err)
	}
	again, err := ResolveTyped(firstScope, key)
	if err != nil || again != first {
		t.Fatalf("同一作用域泛型解析未复用实例: first=%p again=%p err=%v", first, again, err)
	}

	secondScope, err := app.NewScope()
	if err != nil {
		t.Fatalf("创建第二个作用域失败: %v", err)
	}
	defer secondScope.Close()
	second, err := ResolveTyped(secondScope, key)
	if err != nil || second == first || second.sequence != 2 {
		t.Fatalf("泛型 Scoped 服务未跨作用域隔离: first=%p second=%p err=%v", first, second, err)
	}
}

// TestTypedServiceBindingRejectsInvalidContracts 验证空名称、零值键、空解析器和错误实例类型都返回稳定错误。
func TestTypedServiceBindingRejectsInvalidContracts(t *testing.T) {
	if _, err := NewTypedServiceKey[string](" invalid "); !errors.Is(err, ErrInvalidServiceName) {
		t.Fatalf("非法泛型服务名必须被拒绝，实际为 %v", err)
	}
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	var zeroKey TypedServiceKey[string]
	if err := InstanceTyped(app, zeroKey, "invalid"); !errors.Is(err, ErrInvalidServiceName) {
		t.Fatalf("零值泛型服务键必须被拒绝，实际为 %v", err)
	}
	key, err := NewTypedServiceKey[string]("typed.value")
	if err != nil {
		t.Fatalf("创建泛型服务键失败: %v", err)
	}
	if err = InstanceTyped(app, key, "ready"); err != nil {
		t.Fatalf("绑定泛型实例失败: %v", err)
	}
	var nilResolver *ContainerScope
	if _, err = ResolveTyped(nilResolver, key); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("类型化空解析器必须被拒绝，实际为 %v", err)
	}
	app.Instance(key.Name(), 42)
	if _, err = ResolveTyped(app, key); !errors.Is(err, ErrServiceTypeMismatch) {
		t.Fatalf("错误泛型实例类型必须被拒绝，实际为 %v", err)
	}
}

// TestTypedServiceMutationReportsLifecycleRejection 验证运行态和关闭态拒绝注册时，泛型接口不会静默返回成功。
func TestTypedServiceMutationReportsLifecycleRejection(t *testing.T) {
	key, err := NewTypedServiceKey[string]("typed.lifecycle")
	if err != nil {
		t.Fatalf("创建泛型服务键失败: %v", err)
	}
	kernel := &blockingAppRunKernel{started: make(chan struct{}), release: make(chan struct{})}
	app := &App{container: NewContainer(), Kernel: kernel}
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run() }()
	<-kernel.started

	if err = InstanceTyped(app, key, "running"); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("运行态泛型实例注册应返回 ErrApplicationRunning，实际为 %v", err)
	}
	if err = BindTyped(app, key, func(*Container) (string, error) {
		return "running", nil
	}); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("运行态泛型工厂注册应返回 ErrApplicationRunning，实际为 %v", err)
	}

	close(kernel.release)
	if err = <-runDone; err != nil {
		t.Fatalf("应用运行失败: %v", err)
	}
	if err = InstanceTyped(app, key, "closed"); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭态泛型实例注册应返回 ErrApplicationClosed，实际为 %v", err)
	}
}

// TestRawServiceAccessRejectsClosedApplication 验证原始容器入口不会在资源关闭后继续解析或创建作用域。
func TestRawServiceAccessRejectsClosedApplication(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	if err := app.Instance("lifecycle.value", "ready"); err != nil {
		t.Fatalf("注册测试服务失败: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭测试应用失败: %v", err)
	}
	if _, err := app.Make("lifecycle.value"); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭态 Make 应返回 ErrApplicationClosed，实际为 %v", err)
	}
	if _, err := app.NewScope(); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("关闭态 NewScope 应返回 ErrApplicationClosed，实际为 %v", err)
	}
}
