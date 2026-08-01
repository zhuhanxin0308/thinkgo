package route

import (
	"thinkgo/framework"
	"thinkgo/framework/context"
	frameworkRoute "thinkgo/framework/route"
)

// init 注册路由加载器
// Load 注册应用路由，保持路由定义集中在 route 包。
func Load(app *framework.App) error {
	router, err := framework.ResolveServiceAs[*frameworkRoute.Router](app, framework.ServiceRoute)
	if err != nil {
		return err
	}
	home, err := router.Get("/", func(req *context.Request) *context.Response {
		return context.NewResponse().Content("ThinkGo")
	})
	if err != nil {
		return err
	}
	if err = home.WithName("home"); err != nil {
		return err
	}

	// 用户资源入口，控制器由 index 应用注册入口绑定。
	users, err := router.Get("/api/users", "User@Index")
	if err != nil {
		return err
	}
	return users.WithName("users.index")
}
