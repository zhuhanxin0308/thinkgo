package route

import (
	"thinkgo/framework"
	"thinkgo/framework/context"
)

// init 注册路由加载器
func init() {
	framework.MustRegisterRouteLoader(Load)
}

// Load 注册应用路由，保持路由定义集中在 route 包。
func Load(app *framework.App) error {
	home, err := app.Route.Get("/", func(req *context.Request) *context.Response {
		return context.NewResponse().Content("ThinkGo")
	})
	if err != nil {
		return err
	}
	if err = home.WithName("home"); err != nil {
		return err
	}

	// 用户资源入口，控制器由 app/controller 包在 init 中注册。
	users, err := app.Route.Get("/api/users", "User@Index")
	if err != nil {
		return err
	}
	return users.WithName("users.index")
}
