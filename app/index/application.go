package index

import (
	"fmt"

	"thinkgo/app/index/controller"
	appmiddleware "thinkgo/app/index/middleware"
	"thinkgo/app/index/route"
	"thinkgo/framework"
)

// Register 将 index 应用的控制器、中间件和路由加载器注册到传入的 App 实例。
// 应用组件只写入当前 App，避免多个应用之间共享注册表和运行时状态。
func Register(app *framework.App) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	if err := app.RegisterController("User", &controller.User{}); err != nil {
		return fmt.Errorf("注册 User 控制器失败: %w", err)
	}
	if err := app.RegisterGlobalMiddleware(appmiddleware.RequestID); err != nil {
		return fmt.Errorf("注册 RequestID 中间件失败: %w", err)
	}
	if err := app.RegisterRouteLoader(route.Load); err != nil {
		return fmt.Errorf("注册 index 路由加载器失败: %w", err)
	}
	return nil
}

func init() {
	framework.MustRegisterApplication(framework.ApplicationDefinition{
		Name:     "index",
		Path:     "app/index",
		Register: Register,
	})
}
