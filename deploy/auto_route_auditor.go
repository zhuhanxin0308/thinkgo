package deploy

import (
	"context"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// checkAutomaticDispatch 读取正式加载后的路由器，避免仅检查配置文件而遗漏业务覆写。
// 自动路由保留默认兼容行为，但严格部署门禁要求显式声明对外开放的动作。
func checkAutomaticDispatch(_ context.Context, app *framework.App) Result {
	if app == nil {
		return fail("应用为空")
	}
	if err := app.LoadRoutes(); err != nil {
		return fail("加载应用路由失败: " + err.Error())
	}
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		return fail("路由服务不可用: " + err.Error())
	}
	if router.AutoRouteEnabled() {
		return warn("自动路由可调度未显式注册的控制器动作；严格部署要求 route.url_route_must=true 且业务加载器不重新启用自动路由")
	}
	return pass("自动路由已关闭，对外动作由显式路由声明")
}
