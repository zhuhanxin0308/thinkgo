package framework

import (
	"testing"
)

// mockProvider 模拟服务提供者
type mockProvider struct {
	registered bool
	booted     bool
}

func (p *mockProvider) Register(app *App) {
	p.registered = true
}

func (p *mockProvider) Boot(app *App) {
	p.booted = true
}

// TestRegisterProvider 验证服务提供者注册
func TestRegisterProvider(t *testing.T) {
	app := &App{
		Container: NewContainer(),
	}

	provider := &mockProvider{}
	app.RegisterProvider(provider)

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
		Container: NewContainer(),
	}

	p1 := &mockProvider{}
	p2 := &mockProvider{}
	app.RegisterProvider(p1)
	app.RegisterProvider(p2)

	app.BootProviders()

	if !p1.booted || !p2.booted {
		t.Fatal("BootProviders 应调用所有 Provider 的 Boot()")
	}
}

// TestProviderOrder 验证 Register → Boot 顺序
func TestProviderOrder(t *testing.T) {
	app := &App{
		Container: NewContainer(),
	}

	order := make([]string, 0)

	type orderProvider struct {
		name string
	}

	// 使用闭包捕获执行顺序
	p1 := &trackingProvider{name: "db", order: &order}
	p2 := &trackingProvider{name: "cache", order: &order}

	app.RegisterProvider(p1)
	app.RegisterProvider(p2)
	app.BootProviders()

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
		Container: NewContainer(),
	}

	provider := &countingProvider{}
	app.RegisterProvider(provider)
	app.BootProviders()
	app.BootProviders()

	if provider.bootCount != 1 {
		t.Fatalf("同一批 Provider 应只 Boot 一次，实际为 %d 次", provider.bootCount)
	}
}

// TestRegisterProviderConcurrentBootWaitsForRegister 验证并发启动时不会让 Boot 早于 Register 完成。
func TestRegisterProviderConcurrentBootWaitsForRegister(t *testing.T) {
	app := &App{
		Container: NewContainer(),
	}
	provider := newBlockingProvider()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RegisterProvider(provider)
	}()

	<-provider.registerStarted
	app.BootProviders()
	close(provider.allowRegisterFinish)
	<-done

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

func (p *trackingProvider) Register(app *App) {
	*p.order = append(*p.order, "register:"+p.name)
}

func (p *trackingProvider) Boot(app *App) {
	*p.order = append(*p.order, "boot:"+p.name)
}

// countingProvider 统计 Boot 次数的测试 Provider。
type countingProvider struct {
	bootCount int
}

func (p *countingProvider) Register(app *App) {}

func (p *countingProvider) Boot(app *App) {
	p.bootCount++
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

func (p *blockingProvider) Register(app *App) {
	close(p.registerStarted)
	<-p.allowRegisterFinish
	p.registered = true
}

func (p *blockingProvider) Boot(app *App) {
	if !p.registered {
		p.bootBeforeRegister = true
	}
	p.bootCount++
}
