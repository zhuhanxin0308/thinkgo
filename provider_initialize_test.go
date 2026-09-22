package framework

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// initializationStageProvider 用于验证 Provider 初始化阶段的顺序和幂等性。
type initializationStageProvider struct {
	name            string
	order           *[]string
	initializeCount int
	initializeErr   error
	panicInitialize bool
}

func (p *initializationStageProvider) Register(app *App) error {
	*p.order = append(*p.order, "register:"+p.name)
	return nil
}

func (p *initializationStageProvider) Initialize(app *App) error {
	p.initializeCount++
	*p.order = append(*p.order, "initialize:"+p.name)
	if p.panicInitialize {
		panic("initialize panic")
	}
	return p.initializeErr
}

func (p *initializationStageProvider) Boot(app *App) error {
	*p.order = append(*p.order, "boot:"+p.name)
	return nil
}

// TestInitializeProvidersRunsOptionalInitializersOnce 验证初始化阶段按注册顺序且只执行一次。
func TestInitializeProvidersRunsOptionalInitializersOnce(t *testing.T) {
	app := &App{
		container: NewContainer(),
	}
	order := make([]string, 0, 6)
	first := &initializationStageProvider{name: "first", order: &order}
	second := &initializationStageProvider{name: "second", order: &order}

	if err := app.RegisterProvider(first); err != nil {
		t.Fatalf("注册第一个 Provider 失败: %v", err)
	}
	if err := app.RegisterProvider(second); err != nil {
		t.Fatalf("注册第二个 Provider 失败: %v", err)
	}
	if err := app.initializeProviders(); err != nil {
		t.Fatalf("初始化 Provider 失败: %v", err)
	}
	if err := app.initializeProviders(); err != nil {
		t.Fatalf("重复初始化 Provider 失败: %v", err)
	}

	expected := []string{
		"register:first",
		"register:second",
		"initialize:first",
		"initialize:second",
	}
	if len(order) != len(expected) {
		t.Fatalf("Provider 执行次数不正确，期望 %d，实际 %d: %v", len(expected), len(order), order)
	}
	for index, value := range expected {
		if order[index] != value {
			t.Fatalf("Provider 执行顺序[%d]不正确，期望 %s，实际 %s", index, value, order[index])
		}
	}
	if first.initializeCount != 1 || second.initializeCount != 1 {
		t.Fatalf("Provider 初始化应各执行一次，实际 first=%d second=%d", first.initializeCount, second.initializeCount)
	}
}

// TestCacheProviderInitializesDuringApplicationInitialization 验证缓存由内置 Provider 完成初始化。
func TestCacheProviderInitializesDuringApplicationInitialization(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	app := NewAppUninitialized(basePath)
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if app.cache == nil {
		t.Fatal("应用初始化完成后缓存实例不应为空")
	}
	if app.Get("cache") != app.cache {
		t.Fatal("容器中的 cache 实例应与应用内部缓存快照保持一致")
	}
	if app.Get("log") != app.log {
		t.Fatal("容器中的 log 实例应与应用内部日志快照保持一致")
	}
	if app.Get("lang") != app.lang {
		t.Fatal("容器中的 lang 实例应与应用内部语言快照保持一致")
	}
}

// TestCookieAndSessionProvidersAssembleAfterConfigurationLoad 验证 Cookie 和 Session 不依赖后续内联初始化。
func TestCookieAndSessionProvidersAssembleAfterConfigurationLoad(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	app := NewAppUninitialized(basePath)
	if err := app.config.LoadAll(app.BasePath + "/config"); err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	app.applyApplicationRuntimeConfig()
	if err := app.initializeProviders(); err != nil {
		t.Fatalf("初始化配置型 Provider 失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if app.cookie == nil || app.session == nil {
		t.Fatalf("Provider 阶段应完成 Cookie 和 Session 装配: cookie=%v session=%v", app.cookie, app.session)
	}
	if app.Get("cookie") != app.cookie {
		t.Fatal("容器中的 cookie 实例应与应用内部 Cookie 快照保持一致")
	}
	if app.Get("session") != app.session {
		t.Fatal("容器中的 session 实例应与应用内部 Session 快照保持一致")
	}
}

// TestDatabaseProviderAssemblesAfterConfigurationLoad 验证数据库管理器和默认连接属于配置型 Provider 阶段。
func TestDatabaseProviderAssemblesAfterConfigurationLoad(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	setDatabaseStartupPolicyForTest(t, basePath, DatabaseStartupRequired)

	app := NewAppUninitialized(basePath)
	if err := app.config.LoadAll(app.BasePath + "/config"); err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	app.applyApplicationRuntimeConfig()
	if err := app.initializeProviders(); err != nil {
		t.Fatalf("初始化配置型 Provider 失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if app.dbManager == nil || app.db == nil {
		t.Fatalf("Provider 阶段应完成数据库装配: manager=%v db=%v", app.dbManager, app.db)
	}
	if app.Get("db_manager") != app.dbManager {
		t.Fatal("容器中的 db_manager 实例应与应用内部数据库管理器快照保持一致")
	}
	if app.Get("db") != app.db {
		t.Fatal("容器中的 db 实例应与应用内部数据库快照保持一致")
	}
	if app.view == nil {
		t.Fatal("Provider 阶段应完成 View 装配")
	}
	if app.Get("view") != app.view {
		t.Fatal("容器中的 view 实例应与应用内部视图快照保持一致")
	}
}

// TestInitializeProvidersAggregatesErrorsAndProtectsAgainstPanic 验证初始化失败会聚合且 panic 不会穿透应用边界。
func TestInitializeProvidersAggregatesErrorsAndProtectsAgainstPanic(t *testing.T) {
	app := &App{container: NewContainer()}
	firstErr := errors.New("first initialize failed")
	order := make([]string, 0, 3)
	first := &initializationStageProvider{name: "first", order: &order, initializeErr: firstErr}
	second := &initializationStageProvider{name: "second", order: &order, panicInitialize: true}
	third := &initializationStageProvider{name: "third", order: &order}

	for _, provider := range []*initializationStageProvider{first, second, third} {
		if err := app.RegisterProvider(provider); err != nil {
			t.Fatalf("注册 Provider 失败: %v", err)
		}
	}
	firstResult := app.initializeProviders()
	secondResult := app.initializeProviders()
	if !errors.Is(firstResult, firstErr) || !errors.Is(firstResult, ErrProviderCallbackPanic) {
		t.Fatalf("初始化错误应同时包含业务错误和 panic 错误，实际为 %v", firstResult)
	}
	if !errors.Is(secondResult, firstErr) || !errors.Is(secondResult, ErrProviderCallbackPanic) {
		t.Fatalf("重复初始化应返回稳定错误，实际为 %v", secondResult)
	}
	if third.initializeCount != 1 {
		t.Fatalf("前置 Provider 失败不应阻止后续初始化，实际 third=%d", third.initializeCount)
	}
}

// TestProviderRegisteredAfterInitializationRunsInitializer 验证应用初始化后再注册 Provider 时会立即完成初始化。
func TestProviderRegisteredAfterInitializationRunsInitializer(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)

	app := NewAppUninitialized(basePath)
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	order := make([]string, 0, 3)
	provider := &initializationStageProvider{name: "late", order: &order}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("初始化完成后注册 Provider 失败: %v", err)
	}
	if provider.initializeCount != 1 {
		t.Fatalf("初始化完成后注册的 Provider 应立即初始化，实际 %d 次", provider.initializeCount)
	}
	if err := app.BootProviders(); err != nil {
		t.Fatalf("启动延迟注册的 Provider 失败: %v", err)
	}
	expected := []string{"register:late", "initialize:late", "boot:late"}
	if len(order) != len(expected) {
		t.Fatalf("延迟注册 Provider 执行次数不正确，期望 %d，实际 %d: %v", len(expected), len(order), order)
	}
	for index, value := range expected {
		if order[index] != value {
			t.Fatalf("延迟注册 Provider 执行顺序[%d]不正确，期望 %s，实际 %s", index, value, order[index])
		}
	}
}

type blockedInitializeProvider struct {
	initializeStarted chan struct{}
	allowInitialize   chan struct{}
	initializeCount   atomic.Int32
	bootBeforeInit    atomic.Bool
}

func (p *blockedInitializeProvider) Register(*App) error { return nil }

func (p *blockedInitializeProvider) Initialize(*App) error {
	p.initializeCount.Add(1)
	close(p.initializeStarted)
	<-p.allowInitialize
	return nil
}

func (p *blockedInitializeProvider) Boot(*App) error {
	if p.initializeCount.Load() == 0 {
		p.bootBeforeInit.Store(true)
	}
	return nil
}

type shutdownDuringRegistrationProvider struct {
	registerStarted     chan struct{}
	allowRegisterFinish chan struct{}
	initializeStarted   chan struct{}
	allowInitialize     chan struct{}
	initializeCount     atomic.Int32
	shutdownCount       atomic.Int32
	shutdownBeforeInit  atomic.Bool
}

func (p *shutdownDuringRegistrationProvider) Register(*App) error {
	close(p.registerStarted)
	<-p.allowRegisterFinish
	return nil
}

func (p *shutdownDuringRegistrationProvider) Initialize(*App) error {
	p.initializeCount.Add(1)
	close(p.initializeStarted)
	<-p.allowInitialize
	return nil
}

func (p *shutdownDuringRegistrationProvider) Boot(*App) error { return nil }

func (p *shutdownDuringRegistrationProvider) Shutdown(*App) error {
	if p.initializeCount.Load() == 0 {
		p.shutdownBeforeInit.Store(true)
	}
	p.shutdownCount.Add(1)
	return nil
}

// TestProviderShutdownInitializesAdmittedProvider 验证关闭开始后已进入 Register 的 Provider
// 仍会先完成 Initialize，再按生命周期顺序执行 Shutdown。
func TestProviderShutdownInitializesAdmittedProvider(t *testing.T) {
	app := &App{container: NewContainer()}
	order := make([]string, 0, 1)
	if err := app.RegisterProvider(&initializationStageProvider{name: "initial", order: &order}); err != nil {
		t.Fatalf("注册初始 Provider 失败: %v", err)
	}
	if err := app.initializeProviders(); err != nil {
		t.Fatalf("初始化初始 Provider 失败: %v", err)
	}

	provider := &shutdownDuringRegistrationProvider{
		registerStarted:     make(chan struct{}),
		allowRegisterFinish: make(chan struct{}),
		initializeStarted:   make(chan struct{}),
		allowInitialize:     make(chan struct{}),
	}
	registerDone := make(chan error, 1)
	go func() { registerDone <- app.RegisterProvider(provider) }()
	select {
	case <-provider.registerStarted:
	case <-time.After(time.Second):
		t.Fatal("Provider Register 未按预期开始")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.shutdownProviders() }()
	deadline := time.Now().Add(time.Second)
	for {
		app.providers.lock.Lock()
		closing := app.providers.closing
		app.providers.lock.Unlock()
		if closing {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("关闭阶段未及时阻止新的 Provider 注册")
		}
		time.Sleep(time.Millisecond)
	}
	close(provider.allowRegisterFinish)
	if err := <-registerDone; err != nil {
		t.Fatalf("已进入 Register 的 Provider 不应被拒绝: %v", err)
	}
	select {
	case <-provider.initializeStarted:
	case <-time.After(time.Second):
		t.Fatal("关闭阶段未先执行 Provider Initialize")
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("Initialize 未完成时 Shutdown 不应返回: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(provider.allowInitialize)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("关闭 Provider 失败: %v", err)
	}
	if provider.initializeCount.Load() != 1 || provider.shutdownCount.Load() != 1 {
		t.Fatalf("Provider 生命周期次数错误: initialize=%d shutdown=%d", provider.initializeCount.Load(), provider.shutdownCount.Load())
	}
	if provider.shutdownBeforeInit.Load() {
		t.Fatal("Provider 不得在 Initialize 前执行 Shutdown")
	}
}

// TestProviderBootWaitsForInitialize 验证 Boot 和 Shutdown 都不会越过正在执行的 Initialize。
func TestProviderBootWaitsForInitialize(t *testing.T) {
	app := &App{container: NewContainer()}
	provider := &blockedInitializeProvider{
		initializeStarted: make(chan struct{}),
		allowInitialize:   make(chan struct{}),
	}
	if err := app.RegisterProvider(provider); err != nil {
		t.Fatalf("注册 Provider 失败: %v", err)
	}
	initializeDone := make(chan error, 1)
	go func() { initializeDone <- app.initializeProviders() }()
	select {
	case <-provider.initializeStarted:
	case <-time.After(time.Second):
		t.Fatal("Initialize 未按预期开始")
	}
	bootDone := make(chan error, 1)
	go func() { bootDone <- app.BootProviders() }()
	select {
	case err := <-bootDone:
		t.Fatalf("Initialize 未完成时 Boot 不应返回: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(provider.allowInitialize)
	if err := <-initializeDone; err != nil {
		t.Fatalf("Initialize 失败: %v", err)
	}
	if err := <-bootDone; err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if provider.bootBeforeInit.Load() {
		t.Fatal("Boot 不得早于 Initialize")
	}
	if provider.initializeCount.Load() != 1 {
		t.Fatalf("Initialize 应只执行一次，实际 %d 次", provider.initializeCount.Load())
	}
}
