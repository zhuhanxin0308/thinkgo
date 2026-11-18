package framework

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// lifecycleBootProbeProvider 记录失败应用是否仍然绕过闭锁进入 Boot。
type lifecycleBootProbeProvider struct {
	bootCount atomic.Int32
}

func (*lifecycleBootProbeProvider) Register(*App) error { return nil }

func (provider *lifecycleBootProbeProvider) Boot(*App) error {
	provider.bootCount.Add(1)
	return nil
}

// lifecycleBlockingBootProvider 暴露 Provider Boot 进入点，用于验证
// 项目初始化不会与 Provider 阶段交叉执行。
type lifecycleBlockingBootProvider struct {
	started chan struct{}
	release chan struct{}
}

func (*lifecycleBlockingBootProvider) Register(*App) error { return nil }

func (provider *lifecycleBlockingBootProvider) Boot(*App) error {
	close(provider.started)
	<-provider.release
	return nil
}

// lifecycleReadyKernel 验证 App.Run 在进入内核前已经完成完整初始化。
type lifecycleReadyKernel struct {
	app    *App
	called atomic.Bool
}

func (kernel *lifecycleReadyKernel) Run() error {
	kernel.called.Store(true)
	if !kernel.app.Initialized() {
		return errors.New("内核观察到未初始化应用")
	}
	if kernel.app.State() != ApplicationStateRunning {
		return fmt.Errorf("内核观察到错误状态: %s", kernel.app.State())
	}
	if _, err := kernel.app.ResolveService(ServiceCache); err != nil {
		return fmt.Errorf("内核未取得初始化后的缓存服务: %w", err)
	}
	return nil
}

// TestBootProvidersCannotBypassFailedInitialization 验证初始化失败形成稳定闭锁，
// 后续直接调用 BootProviders 也不得启动 Provider 或覆盖 Failed 状态。
func TestBootProvidersCannotBypassFailedInitialization(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	wantErr := errors.New("应用加载失败")
	app := NewConsoleAppUninitialized(basePath)
	provider := &lifecycleBootProbeProvider{}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册启动探针 Provider 失败: %v", err)
	}
	if err := app.RegisterApplicationLoader(func(*App) error { return wantErr }); err != nil {
		t.Fatalf("注册失败应用加载器失败: %v", err)
	}

	if err := app.Initialize(); !errors.Is(err, wantErr) {
		t.Fatalf("初始化应返回应用加载错误，实际为 %v", err)
	}
	if app.State() != ApplicationStateFailed {
		t.Fatalf("初始化失败后状态应保持 Failed，实际为 %s", app.State())
	}
	bootErr := app.BootProviders()
	if !errors.Is(bootErr, wantErr) || !errors.Is(bootErr, ErrApplicationFailed) {
		t.Fatalf("Failed 应用不得绕过闭锁启动 Provider，实际为 %v", bootErr)
	}
	if provider.bootCount.Load() != 0 {
		t.Fatalf("Failed 应用不应执行 Provider Boot，实际为 %d", provider.bootCount.Load())
	}
	if app.State() != ApplicationStateFailed {
		t.Fatalf("Boot 尝试后 Failed 状态不得被覆盖，实际为 %s", app.State())
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭失败应用失败: %v", err)
	}
}

// TestAppRunEnsuresReadyBeforeKernel 验证 App.Run 不再只启动 Provider，
// 而是在调用真实内核前完成配置和运行时服务初始化。
func TestAppRunEnsuresReadyBeforeKernel(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewConsoleAppUninitialized(basePath)
	kernel := &lifecycleReadyKernel{app: app}
	app.Kernel = kernel

	if err := app.Run(); err != nil {
		t.Fatalf("完整应用运行失败: %v", err)
	}
	if !kernel.called.Load() {
		t.Fatal("App.Run 未调用应用内核")
	}
	if app.State() != ApplicationStateClosed {
		t.Fatalf("App.Run 返回后应完成资源关闭，实际为 %s", app.State())
	}
}

// TestInitializeDoesNotOverlapProviderBoot 验证公开 Initialize 与 BootProviders
// 共享同一转换锁：未初始化项目应用调用 BootProviders 会先在同一转换锁内完成 Initialize，
// 后续并发 Initialize 不会与 Provider Boot 交叉，也不会因跳过初始化锁死生命周期。
func TestInitializeDoesNotOverlapProviderBoot(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewConsoleAppUninitialized(basePath)
	provider := &lifecycleBlockingBootProvider{started: make(chan struct{}), release: make(chan struct{})}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册阻塞 Provider 失败: %v", err)
	}
	bootDone := make(chan error, 1)
	go func() { bootDone <- app.BootProviders() }()
	<-provider.started
	initializeDone := make(chan error, 1)
	initializeStarted := make(chan struct{})
	go func() {
		close(initializeStarted)
		initializeDone <- app.Initialize()
	}()
	<-initializeStarted
	overlapped := false
	deadline := time.NewTimer(100 * time.Millisecond)
	poll := time.NewTicker(time.Millisecond)

observationLoop:
	for {
		select {
		case <-poll.C:
			if app.State() == ApplicationStateInitializing {
				overlapped = true
				break observationLoop
			}
		case <-deadline.C:
			break observationLoop
		}
	}
	poll.Stop()
	deadline.Stop()
	close(provider.release)
	bootErr := <-bootDone
	initializeErr := <-initializeDone

	if overlapped {
		t.Fatal("Initialize 不得在 Provider Boot 完成前进入 Initializing")
	}
	if bootErr != nil {
		t.Fatalf("BootProviders 应先完成项目初始化并正常启动，实际为 %v", bootErr)
	}
	if initializeErr != nil {
		t.Fatalf("BootProviders 已完成初始化后重复 Initialize 应幂等成功，实际为 %v", initializeErr)
	}
	if app.State() != ApplicationStateInitialized {
		t.Fatalf("串行完成后应保持 Initialized，实际为 %s", app.State())
	}
	if err := app.EnsureReady(); err != nil {
		t.Fatalf("BootProviders 后调用 EnsureReady 不应失败: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("关闭转换串行测试应用失败: %v", err)
	}
}

// TestUninitializedAppBootCompletesInitializeAndPreservesEnsureReady 验证未初始化应用直接调用
// Boot / BootProviders 时自动先执行 Initialize，后续 EnsureReady 与核心服务解析全部正常。
func TestUninitializedAppBootCompletesInitializeAndPreservesEnsureReady(t *testing.T) {
	for _, tc := range []struct {
		name      string
		construct func(basePath ...string) *App
		useBoot   bool
	}{
		{name: "ConsoleApp/Boot", construct: NewConsoleAppUninitialized, useBoot: true},
		{name: "ConsoleApp/BootProviders", construct: NewConsoleAppUninitialized, useBoot: false},
		{name: "App/Boot", construct: NewAppUninitialized, useBoot: true},
		{name: "App/BootProviders", construct: NewAppUninitialized, useBoot: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			app := tc.construct(basePath)
			t.Cleanup(func() { _ = app.Close() })

			var err error
			if tc.useBoot {
				err = app.Boot()
			} else {
				err = app.BootProviders()
			}
			if err != nil {
				t.Fatalf("未初始化应用调用启动入口失败: %v", err)
			}
			if !app.Initialized() || app.State() != ApplicationStateInitialized {
				t.Fatalf("Boot 后应用必须完成初始化: initialized=%t state=%s", app.Initialized(), app.State())
			}
			if err := app.EnsureReady(); err != nil {
				t.Fatalf("Boot 后调用 EnsureReady 不应锁死或失败: %v", err)
			}
			for _, service := range []ServiceName{ServiceConfig, ServiceLog, ServiceCache} {
				if resolved, resolveErr := app.ResolveService(service); resolveErr != nil || resolved == nil {
					t.Fatalf("解析核心服务 %q 失败: resolved=%v err=%v", service, resolved, resolveErr)
				}
			}
		})
	}

	// 缺少有效配置目录的未初始化应用直接 Boot 应失败闭锁，不得假装 Initialized。
	invalidApp := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = invalidApp.Close() })
	if err := invalidApp.Boot(); err == nil || invalidApp.State() != ApplicationStateFailed {
		t.Fatalf("无效项目直接 Boot 应进入 Failed 状态: state=%s err=%v", invalidApp.State(), err)
	}
}

// TestRunLeaseReferenceCountingAndNilSafety 验证嵌套宿主共享 Running，
// 最后一个租约释放前不允许关闭，且新公开 API 保持 nil 安全。
func TestRunLeaseReferenceCountingAndNilSafety(t *testing.T) {
	var nilApp *App
	if err := nilApp.EnsureReady(); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.EnsureReady 应返回 ErrNilApplication，实际为 %v", err)
	}
	if lease, err := nilApp.AcquireRunLease(); lease != nil || !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.AcquireRunLease 返回错误: lease=%#v err=%v", lease, err)
	}
	var nilLease *RunLease
	nilLease.Release()

	app := &App{}
	first, err := app.AcquireRunLease()
	if err != nil {
		t.Fatalf("获取首个运行租约失败: %v", err)
	}
	second, err := app.AcquireRunLease()
	if err != nil {
		first.Release()
		t.Fatalf("获取嵌套运行租约失败: %v", err)
	}
	first.Release()
	first.Release()
	if app.State() != ApplicationStateRunning {
		t.Fatalf("首个租约释放后嵌套宿主应保持 Running，实际为 %s", app.State())
	}
	if err := app.Close(); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("仍有租约时 Close 应返回 ErrApplicationRunning，实际为 %v", err)
	}
	second.Release()
	if app.State() != ApplicationStateInitialized {
		t.Fatalf("最后一个租约释放后应回到 Initialized，实际为 %s", app.State())
	}
	if err := app.Close(); err != nil {
		t.Fatalf("租约全部释放后关闭应用失败: %v", err)
	}
}
