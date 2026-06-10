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
