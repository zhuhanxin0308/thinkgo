package framework

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

type componentTestController struct{}
type componentTestModel struct{}

type componentTestService struct {
	Service
	registered bool
}

func (service *componentTestService) Register() error {
	service.registered = true
	return nil
}

// TestRegisterApplicationComponentsLoadsDiscoveredTypes 验证统一装配器覆盖
// 中间件、Provider、控制器、模型、验证器和延迟路由加载器。
func TestRegisterApplicationComponentsLoadsDiscoveredTypes(t *testing.T) {
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	handler := middleware.Handler(func(request *Request, next func(*Request) *Response) *Response {
		return next(request)
	})
	applicationService := &componentTestService{}
	err := app.RegisterApplicationComponents(ApplicationComponents{
		Services:    []interface{}{applicationService},
		Middleware:  []middleware.Handler{handler},
		Providers:   map[string]interface{}{string(ServiceRequest): func() string { return "request" }, "service.example": "value"},
		Controllers: map[string]interface{}{"Example": &componentTestController{}},
		Models:      map[string]interface{}{"Example": &componentTestModel{}},
		ValidatorFactories: map[string]interface{}{
			"Example": func() interface{} { return &struct{}{} },
		},
		RouteLoader: func(current *App) error {
			current.Route().Get("/components", func() string { return "ok" })
			return nil
		},
	})
	if err != nil {
		t.Fatalf("装配应用组件失败: %v", err)
	}
	if !app.Has(string(ServiceRequest)) || !app.Has("service.example") || !app.Has(app.ParseClass("validate", "Example")) {
		t.Fatal("应用 Provider 或验证器未注册")
	}
	if !applicationService.registered || app.GetService(applicationService) != applicationService || applicationService.App() != app {
		t.Fatalf("应用服务未绑定到当前应用: registered=%t service=%#v app=%p", applicationService.registered, app.GetService(applicationService), applicationService.App())
	}
	if err := app.LoadRoutes(); err != nil {
		t.Fatalf("加载应用组件路由失败: %v", err)
	}
	routes, err := app.Route().Routes()
	if err != nil || len(routes) != 1 {
		t.Fatalf("应用组件路由错误: routes=%#v err=%v", routes, err)
	}
}

// TestRegisterApplicationComponentsReportsInvalidBoundaries 验证空应用和重复控制器
// 保留稳定错误，生成代码无需自行复制错误处理分支。
func TestRegisterApplicationComponentsReportsInvalidBoundaries(t *testing.T) {
	var nilApp *App
	if err := nilApp.RegisterApplicationComponents(ApplicationComponents{}); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("空应用错误不正确: %v", err)
	}
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterApplicationComponents(ApplicationComponents{Services: []interface{}{nil}}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("非法应用服务错误不正确: %v", err)
	}
	components := ApplicationComponents{Controllers: map[string]interface{}{"Duplicate": &componentTestController{}}}
	if err := app.RegisterApplicationComponents(components); err != nil {
		t.Fatalf("首次装配控制器失败: %v", err)
	}
	if err := app.RegisterApplicationComponents(components); err == nil {
		t.Fatal("重复控制器必须阻断组件装配")
	}
}
