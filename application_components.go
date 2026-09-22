package framework

import (
	"fmt"
	"sort"

	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// ApplicationComponents 描述 service:discover 为单个业务应用发现的组件。
// 生成代码只声明静态清单，排序、错误包装和生命周期校验统一由框架完成。
type ApplicationComponents struct {
	Services           []interface{}
	Events             EventDefinition
	Middleware         []middleware.Handler
	Providers          map[string]interface{}
	Controllers        map[string]interface{}
	Models             map[string]interface{}
	ValidatorFactories map[string]interface{}
	RouteLoader        RouteLoader
}

// RegisterApplicationComponents 按稳定名称顺序装配单个业务应用的全部组件。
func (app *App) RegisterApplicationComponents(components ApplicationComponents) error {
	if app == nil {
		return ErrNilApplication
	}
	for index, service := range components.Services {
		if err := app.Register(service); err != nil {
			return fmt.Errorf("注册第 %d 个应用服务失败: %w", index+1, err)
		}
	}
	if err := app.LoadEvent(components.Events); err != nil {
		return fmt.Errorf("注册应用事件失败: %w", err)
	}
	for _, handler := range components.Middleware {
		if err := app.RegisterApplicationMiddleware(handler); err != nil {
			return fmt.Errorf("注册应用中间件失败: %w", err)
		}
	}
	for _, name := range sortedComponentNames(components.Providers) {
		var err error
		if name == string(ServiceRequest) {
			err = app.BindFactory(name, components.Providers[name])
		} else {
			err = app.Bind(name, components.Providers[name])
		}
		if err != nil {
			return fmt.Errorf("注册应用容器绑定 %q 失败: %w", name, err)
		}
	}
	for _, name := range sortedComponentNames(components.Controllers) {
		if err := app.RegisterController(name, components.Controllers[name]); err != nil {
			return fmt.Errorf("注册控制器 %s 失败: %w", name, err)
		}
	}
	for _, name := range sortedComponentNames(components.Models) {
		if err := app.RegisterModel(name, components.Models[name]); err != nil {
			return fmt.Errorf("注册模型 %s 失败: %w", name, err)
		}
	}
	for _, name := range sortedComponentNames(components.ValidatorFactories) {
		serviceName := app.ParseClass("validate", name)
		if err := app.BindFactory(serviceName, components.ValidatorFactories[name]); err != nil {
			return fmt.Errorf("注册验证器 %s 失败: %w", name, err)
		}
	}
	if components.RouteLoader != nil {
		if err := app.RegisterRouteLoader(components.RouteLoader); err != nil {
			return fmt.Errorf("注册应用路由失败: %w", err)
		}
	}
	return nil
}

func sortedComponentNames(values map[string]interface{}) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
