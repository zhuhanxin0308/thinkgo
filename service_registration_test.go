package framework

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type stagedApplicationService struct {
	register, initialize                    func() error
	boot                                    func()
	shutdown                                func() error
	registered, initialized, booted, closed atomic.Int32
}

func (service *stagedApplicationService) Register() error {
	service.registered.Add(1)
	if service.register != nil {
		return service.register()
	}
	return nil
}

func (service *stagedApplicationService) Initialize() error {
	service.initialized.Add(1)
	if service.initialize != nil {
		return service.initialize()
	}
	return nil
}

func (service *stagedApplicationService) Boot() {
	service.booted.Add(1)
	if service.boot != nil {
		service.boot()
	}
}

type alternateStagedApplicationService struct{ *stagedApplicationService }

func TestApplicationServiceConcurrentRegistrationPreservesBootOrder(t *testing.T) {
	app := newStagedServiceTestApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	var bootOrder []string
	first := &stagedApplicationService{
		register: func() error {
			close(started)
			<-release
			return nil
		},
		boot: func() { bootOrder = append(bootOrder, "first") },
	}
	second := &alternateStagedApplicationService{&stagedApplicationService{
		boot: func() { bootOrder = append(bootOrder, "second") },
	}}
	firstResult := make(chan error, 1)
	go func() { firstResult <- app.Register(first) }()
	<-started
	err := app.Register(second)
	close(release)
	if firstErr := <-firstResult; err != nil || firstErr != nil {
		t.Fatalf("不同类型服务应独立注册: %v, %v", err, firstErr)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatal(err)
	}
	if len(bootOrder) != 2 || bootOrder[0] != "first" || bootOrder[1] != "second" {
		t.Fatalf("启动顺序应与注册预留顺序一致: %v", bootOrder)
	}
}

func TestApplicationServiceForceReplacementBeforeInitialization(t *testing.T) {
	app := newStagedServiceTestApp(t)
	old, next := &stagedApplicationService{}, &stagedApplicationService{}
	if err := app.Register(old); err != nil {
		t.Fatal(err)
	}
	if err := app.Register(next, true); err != nil {
		t.Fatal(err)
	}
	if app.GetService(next) != next || old.closed.Load() != 1 || next.initialized.Load() != 0 {
		t.Fatal("尚未初始化的应用应保留初始化顺序并仅发布新实例")
	}
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	if old.initialized.Load() != 0 || next.initialized.Load() != 1 {
		t.Fatal("应用初始化只应运行替换后的服务")
	}
}

func (service *stagedApplicationService) Shutdown() error {
	service.closed.Add(1)
	if service.shutdown != nil {
		return service.shutdown()
	}
	return nil
}

func TestApplicationServiceRejectsConcurrentDuplicateBeforeRegistrationCompletes(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	started, release := make(chan struct{}), make(chan struct{})
	failure := errors.New("首次注册失败")
	first := &stagedApplicationService{register: func() error {
		close(started)
		<-release
		return failure
	}}
	primary := make(chan error, 1)
	go func() { primary <- app.Register(first) }()
	<-started

	second := &stagedApplicationService{}
	duplicate := make(chan error, 1)
	go func() { duplicate <- app.Register(second) }()
	select {
	case err := <-duplicate:
		if !errors.Is(err, ErrApplicationServiceRegistrationInProgress) {
			t.Errorf("同类型注册尚未完成，应返回进行中错误: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("重复注册应立即返回，不能等待在途回调")
	}
	if got := app.GetService(second); got != nil {
		t.Error("尚未成功注册的服务不能被查询到")
	}
	close(release)
	if err := <-primary; !errors.Is(err, failure) || second.registered.Load() != 0 || app.GetService(second) != nil {
		t.Fatalf("首次失败后服务状态或错误不一致: err=%v", err)
	}
}

func TestApplicationServiceSameTypeReentryReturnsImmediately(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	second := &stagedApplicationService{}
	var nestedErr error
	first := &stagedApplicationService{register: func() error {
		nestedErr = app.Register(second)
		return nil
	}}
	if err := app.Register(first); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(nestedErr, ErrApplicationServiceRegistrationInProgress) || second.registered.Load() != 0 || app.GetService(first) != first {
		t.Fatalf("同类型重入应被明确拒绝且保留首个服务: %v", nestedErr)
	}
}

func TestApplicationServiceForceReplacementPublishesAfterInitialization(t *testing.T) {
	app := newStagedServiceTestApp(t)
	old := &stagedApplicationService{}
	if err := app.Register(old); err != nil {
		t.Fatal(err)
	}
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	next := &stagedApplicationService{
		initialize: func() error {
			close(started)
			<-release
			return nil
		},
		boot: func() {
			if old.closed.Load() != 1 {
				t.Error("新服务 Boot 前，旧服务应已完成释放")
			}
		},
	}
	replacement := make(chan error, 1)
	go func() { replacement <- app.Register(next, true) }()
	select {
	case <-started:
	case err := <-replacement:
		t.Fatalf("候选服务没有进入必要初始化: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("候选服务初始化未开始")
	}
	if app.GetService(next) != old || old.closed.Load() != 0 {
		t.Error("新实例初始化期间必须保留旧服务")
	}
	boot := make(chan error, 1)
	go func() { boot <- app.BootProviders() }()
	close(release)
	if err := <-replacement; err != nil {
		t.Fatal(err)
	}
	if err := <-boot; err != nil {
		t.Fatal(err)
	}
	if app.GetService(next) != next || old.closed.Load() != 1 || old.booted.Load() != 0 ||
		next.registered.Load() != 1 || next.initialized.Load() != 1 || next.booted.Load() != 1 {
		t.Fatal("替换后只应由新实例进入 Boot，旧实例应已关闭一次")
	}
	if err := app.Register(&stagedApplicationService{}, true); !errors.Is(err, ErrProviderRegistrationClosed) {
		t.Fatalf("Boot 之后必须拒绝替换: %v", err)
	}
	if err := app.Close(); err != nil || old.closed.Load() != 1 || next.closed.Load() != 1 {
		t.Fatalf("关闭应用时服务应只释放一次: %v", err)
	}
}

func TestApplicationServiceForceReplacementRollsBackCallbackFailures(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		app := newStagedServiceTestApp(t)
		old := &stagedApplicationService{}
		if err := app.Register(old); err != nil {
			t.Fatal(err)
		}
		if err := app.Initialize(); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("候选服务配置失败")
		next := &stagedApplicationService{}
		if initialize {
			next.initialize = func() error { return failure }
		} else {
			next.register = func() error { return failure }
		}
		if err := app.Register(next, true); !errors.Is(err, failure) {
			t.Fatalf("替换失败应返回原始错误: %v", err)
		}
		if app.GetService(next) != old || old.closed.Load() != 0 || app.StartupError() != nil {
			t.Fatalf("失败的替换污染原有服务或启动状态: %v", app.StartupError())
		}
		if err := app.BootProviders(); err != nil || old.booted.Load() != 1 || next.booted.Load() != 0 {
			t.Fatalf("失败替换后必须仍能启动原服务: %v", err)
		}
	}
}

func TestApplicationServiceForceReplacementKeepsNewServiceIfOldCloseFails(t *testing.T) {
	app := newStagedServiceTestApp(t)
	closeErr := errors.New("旧服务释放失败")
	old := &stagedApplicationService{shutdown: func() error { return closeErr }}
	if err := app.Register(old); err != nil {
		t.Fatal(err)
	}
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	next := &stagedApplicationService{}
	if err := app.Register(next, true); !errors.Is(err, closeErr) {
		t.Fatalf("应保留旧服务的释放错误: %v", err)
	}
	if app.GetService(next) != next || old.closed.Load() != 1 {
		t.Fatal("释放旧实例的错误不应撤销已发布的替换")
	}
	if err := app.Close(); err != nil || old.closed.Load() != 1 || next.closed.Load() != 1 {
		t.Fatalf("关闭时只应释放当前服务: %v", err)
	}
}

func TestApplicationServiceLateInitializationIsSerialized(t *testing.T) {
	app := newStagedServiceTestApp(t)
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	firstStarted, firstRelease := make(chan struct{}), make(chan struct{})
	secondRegistered, secondInitialized := make(chan struct{}), make(chan struct{})
	first := &stagedApplicationService{initialize: func() error {
		close(firstStarted)
		<-firstRelease
		return nil
	}}
	second := &alternateStagedApplicationService{&stagedApplicationService{
		register:   func() error { close(secondRegistered); return nil },
		initialize: func() error { close(secondInitialized); return nil },
	}}
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { firstDone <- app.Register(first) }()
	<-firstStarted
	go func() { secondDone <- app.Register(second) }()
	<-secondRegistered
	select {
	case <-secondInitialized:
		t.Error("后到服务的初始化不能与已有生命周期回调并行")
	case <-time.After(30 * time.Millisecond):
	}
	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil || app.GetService(second) != second {
		t.Fatalf("串行初始化结束后应发布后到服务: %v", err)
	}
}

func TestApplicationServiceForceReplacementDoesNotCloseDependencyDuringInitialization(t *testing.T) {
	app := newStagedServiceTestApp(t)
	dependency := &stagedApplicationService{}
	childStarted, childRelease := make(chan struct{}), make(chan struct{})
	child := &alternateStagedApplicationService{&stagedApplicationService{initialize: func() error {
		close(childStarted)
		<-childRelease
		if dependency.closed.Load() != 0 {
			return errors.New("依赖在初始化期间被关闭")
		}
		return nil
	}}}
	for _, instance := range []interface{}{dependency, child} {
		if err := app.Register(instance); err != nil {
			t.Fatal(err)
		}
	}
	initializeDone := make(chan error, 1)
	go func() { initializeDone <- app.Initialize() }()
	<-childStarted
	next := &stagedApplicationService{}
	err := app.Register(next, true)
	if !errors.Is(err, ErrApplicationServiceRegistrationInProgress) || app.GetService(dependency) != dependency {
		t.Errorf("初始化阶段的替换应返回可重试错误并保留依赖: %v", err)
	}
	close(childRelease)
	if err := <-initializeDone; err != nil {
		t.Fatal(err)
	}
	if err := app.Register(next, true); err != nil || app.GetService(next) != next {
		t.Fatalf("初始化完成且尚未 Boot 时应可重试替换: %v", err)
	}
}

func newStagedServiceTestApp(t *testing.T) *App {
	t.Helper()
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func TestApplicationServiceNestedReplacementDoesNotBlockInitialPhase(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := NewAppUninitialized(basePath)
	old := &stagedApplicationService{}
	if err := app.Register(old); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	next := &stagedApplicationService{register: func() error {
		close(entered)
		<-release
		return nil
	}}
	nestedResult := make(chan error, 1)
	outer := &alternateStagedApplicationService{&stagedApplicationService{register: func() error {
		nestedResult <- app.Register(next, true)
		return nil
	}}}
	registered := make(chan error, 1)
	go func() { registered <- app.Register(outer) }()
	<-entered
	initialized := make(chan error, 1)
	go func() { initialized <- app.Initialize() }()
	waitForServiceRegistrationPhase(t, app, func(state *providerLifecycle) bool { return state.initializing })
	close(release)
	select {
	case err := <-nestedResult:
		if !errors.Is(err, ErrApplicationServiceRegistrationInProgress) {
			t.Errorf("嵌套替换应返回可重试错误: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("嵌套替换与首次初始化相互等待")
	}
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	if err := <-initialized; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if app.GetService(old) != old || old.closed.Load() != 0 {
		t.Fatal("替换退出后旧服务应保持可用")
	}
	if err := app.Register(&stagedApplicationService{}, true); err != nil {
		t.Fatalf("初始化完成后应能重试替换: %v", err)
	}
}

func TestApplicationServiceLateInitializationFailurePreventsConcurrentBoot(t *testing.T) {
	app := newStagedServiceTestApp(t)
	other := &alternateStagedApplicationService{&stagedApplicationService{}}
	if err := app.Register(other); err != nil {
		t.Fatal(err)
	}
	if err := app.Initialize(); err != nil {
		t.Fatal(err)
	}
	wanted := errors.New("迟到应用服务初始化失败")
	started, release := make(chan struct{}), make(chan struct{})
	candidate := &stagedApplicationService{initialize: func() error {
		close(started)
		<-release
		return wanted
	}}
	registered := make(chan error, 1)
	go func() { registered <- app.Register(candidate) }()
	<-started
	booted := make(chan error, 1)
	go func() { booted <- app.BootProviders() }()
	waitForServiceRegistrationPhase(t, app, func(state *providerLifecycle) bool { return state.registrationClosed })
	close(release)
	if err := <-registered; !errors.Is(err, wanted) {
		t.Fatalf("注册应返回原始初始化错误: %v", err)
	}
	if err := <-booted; !errors.Is(err, wanted) || other.booted.Load() != 0 {
		t.Fatalf("等待在途初始化的 Boot 应返回失败且跳过回调: %v", err)
	}
	if err := app.BootProviders(); !errors.Is(err, wanted) || app.GetService(candidate) != nil {
		t.Fatalf("后续 Boot 也应保持失败，且未发布候选: %v", err)
	}
}

func waitForServiceRegistrationPhase(t *testing.T, app *App, reached func(*providerLifecycle) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		app.providers.lock.Lock()
		ready := reached(&app.providers)
		app.providers.lock.Unlock()
		if ready {
			return
		}
		select {
		case <-deadline:
			t.Fatal("等待应用服务生命周期阶段超时")
		case <-ticker.C:
		}
	}
}
