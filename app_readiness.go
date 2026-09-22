package framework

import (
	stdcontext "context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/health"
	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

const (
	// OperationalLivenessPath 是进程存活探针的默认路径。
	OperationalLivenessPath = "/__thinkgo/health/live"
	// OperationalReadinessPath 是应用就绪探针的默认路径。
	OperationalReadinessPath = "/__thinkgo/health/ready"
	// OperationalMetricsPath 是 Prometheus 指标的默认路径。
	OperationalMetricsPath = "/__thinkgo/metrics"
)

// setDatabaseReadinessError 更新默认数据库的就绪状态；是否阻断启动由数据库启动策略决定。
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

// registerDatabaseReadinessCheck 将默认数据库纳入就绪检查，使 degraded 策略不会接收业务流量。
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
	if err := registry.Register("database", func(ctx stdcontext.Context) error {
		return app.probeDatabaseReadiness(ctx)
	}); err != nil {
		app.recordStartupError(fmt.Errorf("注册数据库就绪检查失败: %w", err))
	}
}

// probeDatabaseReadiness 保留未使用惰性连接的启动语义，已连接或失败过的依赖由探针持续验证。
func (app *App) probeDatabaseReadiness(ctx stdcontext.Context) error {
	if app.dbManager == nil {
		return app.databaseReadinessError()
	}
	database, loaded, err := app.dbManager.DefaultIfLoaded()
	if err != nil {
		return err
	}
	if !loaded {
		previous := app.databaseReadinessError()
		if app.databaseStartupPolicy == DatabaseStartupLazy && previous == nil {
			return nil
		}
		database, err = app.dbManager.Default()
		if err != nil {
			app.setDatabaseReadinessError(err)
			return err
		}
	}
	if database == nil {
		return db.ErrDatabaseUnavailable
	}
	err = database.PingContext(ctx)
	app.setDatabaseReadinessError(err)
	return err
}

// IsOperationalPath 只识别当前应用确实启用的框架运维路径，用于有界的独立请求预留槽。
func (app *App) IsOperationalPath(path string) bool {
	if app == nil || !app.operationalRoutesEnabled {
		return false
	}
	switch path {
	case OperationalLivenessPath, OperationalReadinessPath:
		return true
	case OperationalMetricsPath:
		return app.metricsEnabled && app.metrics != nil && app.metrics.Enabled()
	default:
		return false
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

func (app *App) livenessRouteWithHealth(registry *health.Registry, request *fwcontext.Request) *fwcontext.Response {
	if !app.operationalRequestAllowed(request) {
		return operationalAccessDeniedResponse()
	}
	if app == nil || registry == nil {
		return fwcontext.NewResponse().Code(503).Json(map[string]string{"status": "fail"}).CacheControl("no-store")
	}
	report := registry.Liveness()
	return fwcontext.NewResponse().Code(report.HTTPStatus()).Json(report).CacheControl("no-store")
}

func (app *App) readinessRouteWithHealth(registry *health.Registry, request *fwcontext.Request) *fwcontext.Response {
	if !app.operationalRequestAllowed(request) {
		return operationalAccessDeniedResponse()
	}
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

func (app *App) metricsRouteWithRegistry(registry *metrics.Registry, request *fwcontext.Request) *fwcontext.Response {
	if !app.operationalRequestAllowed(request) {
		return operationalAccessDeniedResponse()
	}
	if registry == nil || !registry.Enabled() {
		return fwcontext.NewResponse().Code(404).Content("404 Not Found")
	}
	return fwcontext.NewResponse().Content(registry.Prometheus() + app.requestTaskPrometheus()).ContentType("text/plain; version=0.0.4").CacheControl("no-store")
}

// operationalRequestAllowed 按直连来源地址执行运维端点白名单；反向代理部署应配置可信代理自身网段。
func (app *App) operationalRequestAllowed(request *fwcontext.Request) bool {
	if app == nil {
		return false
	}
	if app.operationalAccess == OperationalAccessPublic {
		return true
	}
	if app.operationalAccess != OperationalAccessRestricted || len(app.operationalAllowedPrefixes) == 0 || request == nil || request.Raw() == nil {
		return false
	}
	host := strings.TrimSpace(request.Raw().RemoteAddr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	address = address.Unmap()
	for _, prefix := range app.operationalAllowedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func operationalAccessDeniedResponse() *fwcontext.Response {
	return fwcontext.NewResponse().Code(http.StatusNotFound).Content("404 Not Found").CacheControl("no-store")
}
