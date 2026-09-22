package framework

import (
	"errors"
	"testing"
)

type thinkPHPStyleService struct {
	Service
	registered bool
	booted     bool
}

func (service *thinkPHPStyleService) Register() {
	service.registered = service.App() != nil
}

func (service *thinkPHPStyleService) Boot() {
	service.booted = service.App() != nil
}

type thinkPHPStyleErrorService struct {
	Service
	err error
}

func (service *thinkPHPStyleErrorService) Register() error {
	return service.err
}

func (service *thinkPHPStyleErrorService) Boot() error {
	return nil
}

// TestAppRegisterSupportsThinkPHPStyleService 验证应用服务只需嵌入 Service 并实现
// 无参数 Register、Boot，不需要接触底层 ServiceProvider 生命周期签名。
func TestAppRegisterSupportsThinkPHPStyleService(t *testing.T) {
	application := NewApp(t.TempDir())
	t.Cleanup(func() { _ = application.Close() })
	service := &thinkPHPStyleService{}
	if err := application.Register(service); err != nil {
		t.Fatalf("注册 ThinkPHP 风格服务失败: %v", err)
	}
	if !service.registered || service.App() != application {
		t.Fatalf("服务 Register 未获得当前 App: %#v", service)
	}
	if application.GetService(service) != service || application.GetService("*framework.thinkPHPStyleService") != service {
		t.Fatal("GetService 应按实例类型或完整类型名返回已注册服务")
	}
	if err := application.BootProviders(); err != nil {
		t.Fatalf("启动 ThinkPHP 风格服务失败: %v", err)
	}
	if !service.booted {
		t.Fatal("服务 Boot 未执行")
	}
}

// TestAppRegisterKeepsThinkPHPDuplicateAndForceSemantics 验证同类型服务默认复用，
// force=true 时才执行新的 Register。
func TestAppRegisterKeepsThinkPHPDuplicateAndForceSemantics(t *testing.T) {
	application := NewApp(t.TempDir())
	t.Cleanup(func() { _ = application.Close() })
	first := &thinkPHPStyleService{}
	second := &thinkPHPStyleService{}
	if err := application.Register(first); err != nil {
		t.Fatalf("注册第一个服务失败: %v", err)
	}
	if err := application.Register(second); err != nil {
		t.Fatalf("重复注册服务失败: %v", err)
	}
	if second.registered {
		t.Fatal("未指定 force 时不应重复执行同类型服务 Register")
	}
	if err := application.Register(second, true); err != nil {
		t.Fatalf("强制注册服务失败: %v", err)
	}
	if !second.registered {
		t.Fatal("force=true 应执行新服务 Register")
	}
}

// TestAppRegisterPropagatesThinkPHPStyleServiceError 验证带 error 返回值的 Go 服务
// 可以保留完整错误链。
func TestAppRegisterPropagatesThinkPHPStyleServiceError(t *testing.T) {
	application := NewApp(t.TempDir())
	t.Cleanup(func() { _ = application.Close() })
	want := errors.New("service register failed")
	if err := application.Register(&thinkPHPStyleErrorService{err: want}); !errors.Is(err, want) {
		t.Fatalf("服务注册错误未保留: %v", err)
	}
}
