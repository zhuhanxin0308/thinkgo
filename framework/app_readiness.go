package framework

import (
	stdcontext "context"
	"fmt"

	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/health"
	"thinkgo/framework/metrics"
	"thinkgo/framework/route"
)

const (
	// OperationalLivenessPath 是进程存活探针的默认路径。
	OperationalLivenessPath = "/__thinkgo/health/live"
	// OperationalReadinessPath 是应用就绪探针的默认路径。
	OperationalReadinessPath = "/__thinkgo/health/ready"
	// OperationalMetricsPath 是 Prometheus 指标的默认路径。
	OperationalMetricsPath = "/__thinkgo/metrics"
)

// setDatabaseReadinessError 更新默认数据库的就绪状态；数据库连接失败不阻断应用启动，但会阻断 readiness。
func (app *App) setDatabaseReadinessError(err error) {
	if app == nil {
		return
	}
	app.databaseReadinessMu.Lock()
	app.databaseReadinessErr = err
	app.databaseReadinessMu.Unlock()
}

// databaseReadinessError 返回默认数据库就绪状态，错误只在进程内保留，HTTP 报告不会序列化错误文本。
func (app *App) databaseReadinessError() error {
	if app == nil {
		return ErrNilApplication
	}
	app.databaseReadinessMu.RLock()
	err := app.databaseReadinessErr
	app.databaseReadinessMu.RUnlock()
	return err
}

// registerDatabaseReadinessCheck 将默认数据库纳入就绪检查，兼容数据库故障时应用继续启动的历史语义。
func (app *App) registerDatabaseReadinessCheck() {
	if app == nil {
		return
	}
	app.registerDatabaseReadinessCheckTo(app.health)
}

func (app *App) registerDatabaseReadinessCheckTo(registry *health.Registry) {
	if app == nil || registry == nil {
		if app != nil {
			app.recordStartupError(fmt.Errorf("注册数据库就绪检查失败: 健康检查注册表为空"))
		}
		return
	}
	if err := registry.Register("database", func(stdcontext.Context) error {
		return app.databaseReadinessError()
	}); err != nil {
		app.recordStartupError(fmt.Errorf("注册数据库就绪检查失败: %w", err))
	}
}

func (app *App) registerStartupReadinessCheckTo(registry *health.Registry) {
	if app == nil || registry == nil {
		if app != nil {
			app.recordStartupError(fmt.Errorf("注册启动就绪检查失败: 健康检查注册表为空"))
		}
		return
	}
	if err := registry.Register("startup", func(stdcontext.Context) error {
		return app.StartupError()
	}); err != nil {
		app.recordStartupError(fmt.Errorf("注册启动就绪检查失败: %w", err))
	}
}

// registerOperationalRoutes 按配置注册存活、就绪和可选指标端点；所有端点仍经过统一 HTTP 中间件。
func (app *App) registerOperationalRoutes() {
	if app == nil {
		return
	}
	app.registerOperationalRoutesTo(app.route, app.health, app.metrics)
}

func (app *App) registerOperationalRoutesTo(router *route.Router, healthRegistry *health.Registry, metricsRegistry *metrics.Registry) {
	if app == nil || router == nil {
		if app != nil {
			app.recordStartupError(fmt.Errorf("注册运维端点失败: 路由器为空"))
		}
		return
	}
	routes := []struct {
		path    string
		handler func(*fwcontext.Request) *fwcontext.Response
	}{
		{path: OperationalLivenessPath, handler: func(request *fwcontext.Request) *fwcontext.Response {
			return app.livenessRouteWithHealth(healthRegistry, request)
		}},
		{path: OperationalReadinessPath, handler: func(request *fwcontext.Request) *fwcontext.Response {
			return app.readinessRouteWithHealth(healthRegistry, request)
		}},
	}
	if app.metricsEnabled && metricsRegistry != nil {
		routes = append(routes, struct {
			path    string
			handler func(*fwcontext.Request) *fwcontext.Response
		}{path: OperationalMetricsPath, handler: func(request *fwcontext.Request) *fwcontext.Response {
			return app.metricsRouteWithRegistry(metricsRegistry, request)
		}})
	}
	for _, operationalRoute := range routes {
		if _, err := router.Get(operationalRoute.path, operationalRoute.handler); err != nil {
			app.recordStartupError(fmt.Errorf("注册运维端点 %q 失败: %w", operationalRoute.path, err))
		}
	}
}

func (app *App) livenessRouteWithHealth(registry *health.Registry, _ *fwcontext.Request) *fwcontext.Response {
	if app == nil || registry == nil {
		return fwcontext.NewResponse().Code(503).Json(map[string]string{"status": "fail"}).CacheControl("no-store")
	}
	report := registry.Liveness()
	return fwcontext.NewResponse().Code(report.HTTPStatus()).Json(report).CacheControl("no-store")
}

func (app *App) readinessRouteWithHealth(registry *health.Registry, request *fwcontext.Request) *fwcontext.Response {
	if app == nil || registry == nil {
		return fwcontext.NewResponse().Code(503).Json(map[string]string{"status": "fail"}).CacheControl("no-store")
	}
	parent := stdcontext.Background()
	if request != nil {
		parent = request.Context()
	}
	report := registry.Readiness(parent)
	return fwcontext.NewResponse().Code(report.HTTPStatus()).Json(report).CacheControl("no-store")
}

func (app *App) metricsRouteWithRegistry(registry *metrics.Registry, _ *fwcontext.Request) *fwcontext.Response {
	if registry == nil || !registry.Enabled() {
		return fwcontext.NewResponse().Code(404).Content("404 Not Found")
	}
	return fwcontext.NewResponse().Content(registry.Prometheus()).ContentType("text/plain; version=0.0.4").CacheControl("no-store")
}
