package framework

import (
	"errors"
	"fmt"

	"thinkgo/framework/config"
	"thinkgo/framework/cookie"
	"thinkgo/framework/debug"
	"thinkgo/framework/health"
	"thinkgo/framework/lang"
	"thinkgo/framework/log"
	"thinkgo/framework/metrics"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
	"thinkgo/framework/session"
)

// applicationAssemblyServices 保存应用装配阶段解析出的服务快照，避免装配逻辑继续读取 App 的公开字段。
type applicationAssemblyServices struct {
	config     *config.Config
	cookie     *cookie.Cookie
	debug      *debug.Debug
	health     *health.Registry
	lang       *lang.Lang
	log        *log.Log
	metrics    *metrics.Registry
	middleware *middleware.Pipeline
	route      *route.Router
	session    *session.Session
}

// resolveApplicationAssemblyServices 在运行时装配开始前一次性解析所需服务。
func (app *App) resolveApplicationAssemblyServices() (applicationAssemblyServices, error) {
	services := applicationAssemblyServices{}
	var resolveErr error

	services.config, resolveErr = resolveAssemblyService[*config.Config](app, ServiceConfig, resolveErr)
	services.cookie, resolveErr = resolveAssemblyService[*cookie.Cookie](app, ServiceCookie, resolveErr)
	services.lang, resolveErr = resolveAssemblyService[*lang.Lang](app, ServiceLang, resolveErr)
	services.log, resolveErr = resolveAssemblyService[*log.Log](app, ServiceLog, resolveErr)
	services.middleware, resolveErr = resolveAssemblyService[*middleware.Pipeline](app, ServiceMiddleware, resolveErr)
	services.health, resolveErr = resolveAssemblyService[*health.Registry](app, ServiceHealth, resolveErr)
	services.metrics, resolveErr = resolveAssemblyService[*metrics.Registry](app, ServiceMetrics, resolveErr)
	services.route, resolveErr = resolveAssemblyService[*route.Router](app, ServiceRoute, resolveErr)
	if app.sessionEnabled {
		services.session, resolveErr = resolveAssemblyService[*session.Session](app, ServiceSession, resolveErr)
	}
	if app.traceEnabled {
		services.debug, resolveErr = resolveAssemblyService[*debug.Debug](app, ServiceDebug, resolveErr)
	}
	if resolveErr != nil {
		return applicationAssemblyServices{}, resolveErr
	}
	return services, nil
}

func resolveAssemblyService[T any](app *App, name ServiceName, previous error) (T, error) {
	service, err := ResolveServiceAs[T](app, name)
	if err != nil {
		return service, errors.Join(previous, fmt.Errorf("解析应用装配服务 %q 失败: %w", name, err))
	}
	return service, previous
}

// initializeApplicationAssembly 执行依赖应用注册回调的运行时装配阶段。
//
// 该阶段必须位于全部 Provider Initialize 之后，确保自定义 Provider 已有机会注册
// 控制器、中间件和路由，再由框架统一生成最终运行时结构。
func (app *App) initializeApplicationAssembly() {
	app.closeApplicationRegistration()
	services, err := app.resolveApplicationAssemblyServices()
	if err != nil {
		app.recordStartupError(fmt.Errorf("解析应用装配服务失败: %w", err))
		return
	}
	app.initializeControllerBindings()
	app.initializeMiddleware(services)
	app.initializeRoutes(services)
}

// initializeControllerBindings 将控制器类型绑定为工厂，保证每次请求获得独立实例。
func (app *App) initializeControllerBindings() {
	for name, controllerType := range app.snapshotControllerRegistry() {
		app.BindFactory(name, controllerType)
	}
}

// initializeMiddleware 装配框架级和应用级中间件。
func (app *App) initializeMiddleware(services applicationAssemblyServices) {
	// Recovery 必须先注册，保证后续中间件和控制器异常都能进入统一处理链。
	recovery := &middleware.Recovery{
		App:    app,
		Log:    services.log,
		TplDir: app.ExceptionTemplatePath(),
	}
	services.middleware.Alias("recovery", recovery.Handle)
	if err := services.middleware.PipeByNameStrict("recovery"); err != nil {
		app.recordStartupError(fmt.Errorf("注册 recovery 中间件失败: %w", err))
	}

	if app.sessionEnabled {
		sessionMiddleware := &middleware.Session{Manager: services.session}
		services.middleware.Alias("session", sessionMiddleware.Handle)
		if err := services.middleware.PipeByNameStrict("session"); err != nil {
			app.recordStartupError(fmt.Errorf("注册 session 中间件失败: %w", err))
		}
	}

	if app.traceEnabled {
		trace := &middleware.Trace{Debug: services.debug, Location: app.Location()}
		services.middleware.Alias("trace", trace.Handle)
		if err := services.middleware.PipeByNameStrict("trace"); err != nil {
			app.recordStartupError(fmt.Errorf("注册 trace 中间件失败: %w", err))
		}
	}

	// CSRF 别名始终存在；配置错误时使用拒绝请求的退化处理器。
	csrfHandler, csrfErr := createAppCSRF(services.config.GetMap("csrf"), services.cookie)
	if csrfErr != nil {
		app.recordStartupError(fmt.Errorf("初始化 CSRF 失败: %w", csrfErr))
		csrfHandler = unavailableCSRFHandler
	}
	services.middleware.Alias("csrf", csrfHandler)
	if app.csrfEnabled {
		if err := services.middleware.PipeByNameStrict("csrf"); err != nil {
			app.recordStartupError(fmt.Errorf("注册 CSRF 中间件失败: %w", err))
		}
	}

	services.middleware.Alias("lang", loadLangPack(services.lang))
	if err := services.middleware.PipeByNameStrict("lang"); err != nil {
		app.recordStartupError(fmt.Errorf("注册 lang 中间件失败: %w", err))
	}

	for _, handler := range app.snapshotMiddlewareRegistry() {
		services.middleware.Pipe(handler)
	}
	app.applyMiddlewareConfigTo(services.config, services.middleware)
}

// initializeRoutes 应用路由配置、路由加载器、运维路由和启动就绪检查。
func (app *App) initializeRoutes(services applicationAssemblyServices) {
	app.applyRouteConfigTo(services.config, services.route)
	for _, loader := range app.snapshotRouteRegistry() {
		if err := safeLoadRoutes(loader, app); err != nil {
			app.recordStartupError(fmt.Errorf("加载应用路由失败: %w", err))
		}
	}
	if app.operationalRoutesEnabled {
		app.registerOperationalRoutesTo(services.route, services.health, services.metrics)
	}
	app.registerStartupReadinessCheckTo(services.health)
	app.warnProductionSecurity()
}
