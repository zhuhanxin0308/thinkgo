package framework

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/event"
)

// mockProvider 模拟服务提供者
type mockProvider struct {
	registered bool
	booted     bool
}

func (p *mockProvider) Register(app *App) error {
	p.registered = true
	return nil
}

func (p *mockProvider) Boot(app *App) error {
	p.booted = true
	return nil
}

// TestRegisterProvider 验证服务提供者注册
func TestRegisterProvider(t *testing.T) {
	app := &App{
		container: NewContainer(),
	}

	provider := &mockProvider{}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册 Provider 失败: %v", err)
	}

	if !provider.registered {
		t.Fatal("RegisterProvider 应调用 Register()")
	}
	if provider.booted {
		t.Fatal("RegisterProvider 不应调用 Boot()")
	}
}

// TestBootProviders 验证服务提供者启动
func TestBootProviders(t *testing.T) {
	app := &App{
		container: NewContainer(),
	}

	p1 := &mockProvider{}
	p2 := &mockProvider{}
	if err := app.RegisterProvider(p1); err != nil {
		t.Fatalf("注册第一个 Provider 失败: %v", err)
	}
	if err := app.RegisterProvider(p2); err != nil {
		t.Fatalf("注册第二个 Provider 失败: %v", err)
	}

	if err := app.BootProviders(); err != nil {
		t.Fatalf("启动 Provider 失败: %v", err)
	}

	if !p1.booted || !p2.booted {
		t.Fatal("BootProviders 应调用所有 Provider 的 Boot()")
	}
}

// TestApplicationStateTracksProviderBoot 验证 Provider 启动会更新应用生命周期状态。
func TestApplicationStateTracksProviderBoot(t *testing.T) {
	app := &App{
		container: NewContainer(),
		lifecycle: appLifecycle{state: ApplicationStateConstructed},
	}
	if err := app.RegisterProvider(&mockProvider{}); err != nil {
		t.Fatalf("注册状态测试 Provider 失败: %v", err)
	}
	if app.State() != ApplicationStateConstructed {
		t.Fatalf("注册 Provider 后状态不应提前变化: %v", app.State())
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("启动状态测试 Provider 失败: %v", err)
	}
	if app.State() != ApplicationStateInitialized {
		t.Fatalf("Provider 启动后状态错误: %v", app.State())
	}
}

// TestProviderOrder 验证 Register → Boot 顺序
func TestProviderOrder(t *testing.T) {
	app := &App{
		container: NewContainer(),
	}

	order := make([]string, 0)

	// 使用闭包捕获执行顺序
	p1 := &trackingProvider{name: "db", order: &order}
	p2 := &trackingProvider{name: "cache", order: &order}

	if err := app.RegisterProvider(p1); err != nil {
		t.Fatalf("注册 db Provider 失败: %v", err)
	}
	if err := app.RegisterProvider(p2); err != nil {
		t.Fatalf("注册 cache Provider 失败: %v", err)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("启动 Provider 失败: %v", err)
	}

	expected := []string{"register:db", "register:cache", "boot:db", "boot:cache"}
	if len(order) != len(expected) {
		t.Fatalf("执行顺序长度不正确，期望 %d，实际 %d", len(expected), len(order))
	}
	for i, v := range expected {
		if order[i] != v {
			t.Fatalf("执行顺序[%d] 期望 %s，实际 %s", i, v, order[i])
		}
	}
}

// TestBootProvidersIsIdempotent 验证 Provider 启动阶段只执行一次，避免重复注册事件、路由等副作用。
func TestBootProvidersIsIdempotent(t *testing.T) {
	app := &App{
		container: NewContainer(),
	}

	provider := &countingProvider{}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册 Provider 失败: %v", err)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("首次启动 Provider 失败: %v", err)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("重复启动 Provider 应返回首次结果，实际为 %v", err)
	}

	if provider.bootCount != 1 {
		t.Fatalf("同一批 Provider 应只 Boot 一次，实际为 %d 次", provider.bootCount)
	}
}

// TestRegisterProviderConcurrentBootWaitsForRegister 验证并发启动时不会让 Boot 早于 Register 完成。
func TestRegisterProviderConcurrentBootWaitsForRegister(t *testing.T) {
	app := &App{
		container: NewContainer(),
	}
	provider := newBlockingProvider()

	registerDone := make(chan error, 1)
	go func() {
		registerDone <- app.RegisterProvider(provider)
	}()

	<-provider.registerStarted
	bootDone := make(chan error, 1)
	go func() {
		bootDone <- app.BootProviders()
	}()
	select {
	case err := <-bootDone:
		t.Fatalf("存在进行中的 Register 时 BootProviders 不应提前返回，实际为 %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(provider.allowRegisterFinish)
	if err := <-registerDone; err != nil {
		t.Fatalf("并发注册 Provider 失败: %v", err)
	}
	if err := <-bootDone; err != nil {
		t.Fatalf("并发启动 Provider 失败: %v", err)
	}

	if provider.bootBeforeRegister {
		t.Fatal("Provider 的 Boot 不应早于 Register 完成")
	}
	if provider.bootCount != 1 {
		t.Fatalf("Provider 应启动一次，实际为 %d 次", provider.bootCount)
	}
}

// trackingProvider 追踪执行顺序的测试 Provider
type trackingProvider struct {
	name  string
	order *[]string
}

func (p *trackingProvider) Register(app *App) error {
	*p.order = append(*p.order, "register:"+p.name)
	return nil
}

func (p *trackingProvider) Boot(app *App) error {
	*p.order = append(*p.order, "boot:"+p.name)
	return nil
}

// countingProvider 统计 Boot 次数的测试 Provider。
type countingProvider struct {
	bootCount int
}

func (p *countingProvider) Register(app *App) error { return nil }

func (p *countingProvider) Boot(app *App) error {
	p.bootCount++
	return nil
}

type blockingProvider struct {
	registerStarted     chan struct{}
	allowRegisterFinish chan struct{}
	registered          bool
	bootBeforeRegister  bool
	bootCount           int
}

func newBlockingProvider() *blockingProvider {
	return &blockingProvider{
		registerStarted:     make(chan struct{}),
		allowRegisterFinish: make(chan struct{}),
	}
}

func (p *blockingProvider) Register(app *App) error {
	close(p.registerStarted)
	<-p.allowRegisterFinish
	p.registered = true
	return nil
}

func (p *blockingProvider) Boot(app *App) error {
	if !p.registered {
		p.bootBeforeRegister = true
	}
	p.bootCount++
	return nil
}

type failingProvider struct {
	registerErr error
	bootErr     error
}

func (p *failingProvider) Register(app *App) error { return p.registerErr }
func (p *failingProvider) Boot(app *App) error     { return p.bootErr }

// TestProviderErrorsAndDuplicatesFailClosed 验证 Provider 错误、重复与启动后的迟到注册都可观测。
func TestProviderErrorsAndDuplicatesFailClosed(t *testing.T) {
	app := &App{container: NewContainer()}
	var nilProvider *failingProvider
	if err := app.RegisterProvider(nilProvider); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("类型化 nil Provider 应返回 ErrInvalidProvider，实际为 %v", err)
	}
	registerErr := errors.New("register failed")
	err := app.RegisterProvider(&failingProvider{registerErr: registerErr})
	if !errors.Is(err, registerErr) || !strings.Contains(err.Error(), "服务提供者") {
		t.Fatalf("Register 错误应返回调用方，实际为 %v", err)
	}
	app.providers.lock.Lock()
	failedRegistrationCount := len(app.providers.providers)
	app.providers.lock.Unlock()
	if failedRegistrationCount != 0 {
		t.Fatalf("失败的 Register 不应滞留生命周期记录，实际为 %d", failedRegistrationCount)
	}

	provider := &failingProvider{}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册 Provider 失败: %v", err)
	}
	if err := app.RegisterProvider(provider); !errors.Is(err, ErrDuplicateProvider) {
		t.Fatalf("重复 Provider 应返回 ErrDuplicateProvider，实际为 %v", err)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("启动 Provider 失败: %v", err)
	}
	if err := app.RegisterProvider(&failingProvider{}); !errors.Is(err, ErrProviderRegistrationClosed) {
		t.Fatalf("启动后注册应返回 ErrProviderRegistrationClosed，实际为 %v", err)
	}
}

// TestProviderBootFailureIsStableAndPanicSafe 验证 Boot 失败会缓存，panic 会转换为错误而非击穿进程。
func TestProviderBootFailureIsStableAndPanicSafe(t *testing.T) {
	bootErr := errors.New("boot failed")
	app := &App{container: NewContainer()}
	if err := app.RegisterProvider(&failingProvider{bootErr: bootErr}); err != nil {
		t.Fatalf("注册失败 Provider 失败: %v", err)
	}
	first := app.BootProviders()
	second := app.BootProviders()
	if !errors.Is(first, bootErr) || !errors.Is(second, bootErr) {
		t.Fatalf("重复 Boot 应返回同一失败结果，first=%v second=%v", first, second)
	}
	if !strings.Contains(first.Error(), "服务提供者") {
		t.Fatalf("Boot 错误应包含可读的服务提供者上下文，实际为 %v", first)
	}

	panicApp := &App{container: NewContainer()}
	panicProvider := &callbackProvider{register: func() { panic("register panic") }}
	if err := panicApp.RegisterProvider(panicProvider); !errors.Is(err, ErrProviderCallbackPanic) {
		t.Fatalf("Register panic 应转换为 ErrProviderCallbackPanic，实际为 %v", err)
	}
	bootPanicApp := &App{container: NewContainer()}
	if err := bootPanicApp.RegisterProvider(&callbackProvider{boot: func() { panic("boot panic") }}); err != nil {
		t.Fatalf("注册 Boot panic Provider 失败: %v", err)
	}
	if err := bootPanicApp.BootProviders(); !errors.Is(err, ErrProviderCallbackPanic) {
		t.Fatalf("Boot panic 应转换为 ErrProviderCallbackPanic，实际为 %v", err)
	}
	lifecycleErr := errors.New("app init listener failed")
	eventApp := &App{container: NewContainer(), event: event.NewDispatcher()}
	if err := eventApp.event.Listen(event.EventAppInit, &event.SimpleListener{Handler: func(event.Event) error {
		return lifecycleErr
	}}); err != nil {
		t.Fatalf("注册生命周期监听器失败: %v", err)
	}
	if err := eventApp.BootProviders(); !errors.Is(err, lifecycleErr) {
		t.Fatalf("AppInit 监听器错误应阻止 Provider 启动，实际为 %v", err)
	}
}

type callbackProvider struct {
	register func()
	boot     func()
}

func (p *callbackProvider) Register(app *App) error {
	if p.register != nil {
		p.register()
	}
	return nil
}

func (p *callbackProvider) Boot(app *App) error {
	if p.boot != nil {
		p.boot()
	}
	return nil
}

// TestConcurrentBootProvidersWaitsForFirstBoot 验证并发重复启动会等待首次 Boot 完成并共享结果。
func TestConcurrentBootProvidersWaitsForFirstBoot(t *testing.T) {
	app := &App{container: NewContainer()}
	bootStarted := make(chan struct{})
	allowBootFinish := make(chan struct{})
	provider := &callbackProvider{boot: func() {
		close(bootStarted)
		<-allowBootFinish
	}}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册阻塞 Provider 失败: %v", err)
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- app.BootProviders() }()
	<-bootStarted
	go func() { secondDone <- app.BootProviders() }()
	select {
	case err := <-secondDone:
		t.Fatalf("第二次 BootProviders 不应在首次 Boot 完成前返回，实际为 %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowBootFinish)
	if err := <-firstDone; err != nil {
		t.Fatalf("首次 BootProviders 失败: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("第二次 BootProviders 应复用首次结果，实际为 %v", err)
	}
}

// TestConcurrentProviderRegistrationPreservesAdmissionOrder 验证并发 Register 完成顺序不会改变 Boot 顺序。
func TestConcurrentProviderRegistrationPreservesAdmissionOrder(t *testing.T) {
	app := &App{container: NewContainer()}
	firstRegisterStarted := make(chan struct{})
	allowFirstRegister := make(chan struct{})
	bootOrder := make([]string, 0, 2)
	first := &callbackProvider{
		register: func() {
			close(firstRegisterStarted)
			<-allowFirstRegister
		},
		boot: func() { bootOrder = append(bootOrder, "first") },
	}
	second := &callbackProvider{boot: func() { bootOrder = append(bootOrder, "second") }}
	firstDone := make(chan error, 1)
	go func() { firstDone <- app.RegisterProvider(first) }()
	<-firstRegisterStarted
	if err := app.RegisterProvider(second); err != nil {
		t.Fatalf("注册第二个 Provider 失败: %v", err)
	}
	bootDone := make(chan error, 1)
	go func() { bootDone <- app.BootProviders() }()
	close(allowFirstRegister)
	if err := <-firstDone; err != nil {
		t.Fatalf("注册第一个 Provider 失败: %v", err)
	}
	if err := <-bootDone; err != nil {
		t.Fatalf("启动 Provider 失败: %v", err)
	}
	if got := fmt.Sprint(bootOrder); got != "[first second]" {
		t.Fatalf("Boot 应按 Register 准入顺序执行，实际为 %s", got)
	}
}

// TestProviderLifecycleEventsUseDeterministicOrder 验证 AppInit 位于全部 Register 后、Boot 前，
// RouteLoaded 由首次路由加载在 Boot 后触发。
func TestProviderLifecycleEventsUseDeterministicOrder(t *testing.T) {
	app := &App{container: NewContainer(), event: event.NewDispatcher()}
	order := make([]string, 0, 4)
	provider := &callbackProvider{
		register: func() {
			order = append(order, "register")
			if err := app.event.Listen(event.EventAppInit, &event.SimpleListener{Handler: func(event.Event) error {
				order = append(order, "app_init")
				return nil
			}}); err != nil {
				panic(err)
			}
			if err := app.event.Listen(event.EventRouteLoaded, &event.SimpleListener{Handler: func(event.Event) error {
				order = append(order, "route_loaded")
				return nil
			}}); err != nil {
				panic(err)
			}
		},
		boot: func() { order = append(order, "boot") },
	}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册 Provider 失败: %v", err)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("启动 Provider 失败: %v", err)
	}
	if err := app.LoadRoutes(); err != nil {
		t.Fatalf("加载路由失败: %v", err)
	}
	if got := fmt.Sprint(order); got != "[register app_init boot route_loaded]" {
		t.Fatalf("Provider 生命周期事件顺序错误: %s", got)
	}
}

type shutdownTrackingProvider struct {
	name  string
	order *[]string
	err   error
}

func (p *shutdownTrackingProvider) Register(app *App) error { return nil }
func (p *shutdownTrackingProvider) Boot(app *App) error     { return nil }
func (p *shutdownTrackingProvider) Shutdown(app *App) error {
	*p.order = append(*p.order, p.name)
	return p.err
}

// TestProviderShutdownUsesReverseOrderAndAggregatesErrors 验证依赖资源按注册逆序释放且错误不丢失。
func TestProviderShutdownUsesReverseOrderAndAggregatesErrors(t *testing.T) {
	firstErr := errors.New("first shutdown failed")
	secondErr := errors.New("second shutdown failed")
	order := make([]string, 0, 2)
	app := &App{container: NewContainer()}
	first := &shutdownTrackingProvider{name: "first", order: &order, err: firstErr}
	second := &shutdownTrackingProvider{name: "second", order: &order, err: secondErr}
	if err := app.RegisterProvider(first); err != nil {
		t.Fatalf("注册第一个 Provider 失败: %v", err)
	}
	if err := app.RegisterProvider(second); err != nil {
		t.Fatalf("注册第二个 Provider 失败: %v", err)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("启动 Provider 失败: %v", err)
	}
	closeErr := app.Close()
	if !errors.Is(closeErr, firstErr) || !errors.Is(closeErr, secondErr) {
		t.Fatalf("Close 应聚合 Provider Shutdown 错误，实际为 %v", closeErr)
	}
	if got := fmt.Sprint(order); got != "[second first]" {
		t.Fatalf("Provider 应按注册逆序关闭，实际为 %s", got)
	}
	_ = app.Close()
	if len(order) != 2 {
		t.Fatalf("重复 Close 不应重复 Shutdown，实际为 %#v", order)
	}
}
