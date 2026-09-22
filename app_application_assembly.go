package framework

import (
	"errors"
	"fmt"
	"sort"

	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/cookie"
	"github.com/zhuhanxin0308/thinkgo/framework/debug"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/health"
	"github.com/zhuhanxin0308/thinkgo/framework/lang"
	"github.com/zhuhanxin0308/thinkgo/framework/log"
	"github.com/zhuhanxin0308/thinkgo/framework/metrics"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
	"github.com/zhuhanxin0308/thinkgo/framework/session"
)

// applicationAssemblyServices 保存应用装配阶段解析出的服务快照，避免装配逻辑继续读取 App 的公开字段。
type applicationAssemblyServices struct {
	config     *config.Config
	cookie     *cookie.Cookie
	debug      *debug.Debug
	exception  exception.Handler
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
	services.exception, resolveErr = resolveAssemblyService[exception.Handler](app, ServiceExceptionHandle, resolveErr)
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
	if err := app.initializeControllerBindings(); err != nil {
		app.recordStartupError(fmt.Errorf("装配控制器工厂失败: %w", err))
	}
	if err := app.initializeModelBindings(); err != nil {
		app.recordStartupError(fmt.Errorf("装配模型工厂失败: %w", err))
	}
	app.initializeMiddleware(services)
	app.initializeRoutes(services)
}

// initializeControllerBindings 将控制器类型绑定为工厂，保证每次请求获得独立实例。
func (app *App) initializeControllerBindings() error {
	controllers := app.snapshotControllerRegistry()
	names := make([]string, 0, len(controllers))
	for name := range controllers {
		names = append(names, name)
	}
	sort.Strings(names)
	var bindErr error
	for _, name := range names {
		if err := app.BindFactory(name, controllers[name]); err != nil {
			bindErr = errors.Join(bindErr, fmt.Errorf("绑定控制器 %q 失败: %w", name, err))
		}
	}
	return bindErr
}

// initializeMiddleware 装配框架级和应用级中间件。
func (app *App) initializeMiddleware(services applicationAssemblyServices) {
	// Recovery 必须先注册，保证后续中间件和控制器异常都能进入统一处理链。
	recovery := &middleware.Recovery{
		App:     app,
		Log:     services.log,
		TplDir:  app.ExceptionTemplatePath(),
		Handler: services.exception,
	}
	services.middleware.Alias("recovery", recovery.Handle)
	if err := services.middleware.PipeByNameStrict("recovery"); err != nil {
		app.recordStartupError(fmt.Errorf("注册 recovery 中间件失败: %w", err))
	}

	// CORS 位于所有可能短路请求的业务中间件之前，确保预检不被鉴权、限流或 Session 误拦截。
	corsHandler, corsEnabled, corsErr := createAppCors(services.config.GetMap("cors"))
	if corsErr != nil {
		app.recordStartupError(fmt.Errorf("初始化 CORS 失败: %w", corsErr))
		corsHandler = unavailableCorsHandler
	}
	services.middleware.Alias("cors", corsHandler)
	if corsEnabled {
		if err := services.middleware.PipeByNameStrict("cors"); err != nil {
			app.recordStartupError(fmt.Errorf("注册 cors 中间件失败: %w", err))
		}
	}

	securityHandler, securityEnabled, securityErr := createAppSecurityHeaders(services.config.GetMap("security_headers"))
	if securityErr != nil {
		app.recordStartupError(fmt.Errorf("初始化安全响应头失败: %w", securityErr))
	} else if securityEnabled {
		services.middleware.Alias("security_headers", securityHandler)
		if err := services.middleware.PipeByNameStrict("security_headers"); err != nil {
			app.recordStartupError(fmt.Errorf("注册安全响应头中间件失败: %w", err))
		}
	}

	rateLimiter, rateLimitEnabled, rateLimitErr := createAppRateLimit(services.config.GetMap("rate_limit"))
	if rateLimitErr != nil {
		app.recordStartupError(fmt.Errorf("初始化限流失败: %w", rateLimitErr))
	} else if rateLimitEnabled {
		services.middleware.Alias("rate_limit", rateLimiter.Handle)
		if err := services.middleware.PipeByNameStrict("rate_limit"); err != nil {
			app.recordStartupError(fmt.Errorf("注册限流中间件失败: %w", err))
		}
	}

	if app.sessionEnabled {
		sessionMiddleware := &middleware.Session{Manager: services.session}
		services.middleware.Alias("session", sessionMiddleware.Handle)
		if err := services.middleware.PipeByNameStrict("session"); err != nil {
			app.recordStartupError(fmt.Errorf("注册 session 中间件失败: %w", err))
		}
	}

	if app.traceEnabled {
		trace, traceErr := createAppTrace(services.config.GetMap("trace"), services.debug, app)
		if traceErr != nil {
			app.recordStartupError(fmt.Errorf("初始化 trace 中间件失败: %w", traceErr))
		} else {
			services.middleware.Alias("trace", trace.Handle)
			if err := services.middleware.PipeByNameStrict("trace"); err != nil {
				app.recordStartupError(fmt.Errorf("注册 trace 中间件失败: %w", err))
			}
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
	for _, handler := range app.snapshotApplicationMiddlewareRegistry() {
		app.applicationMiddleware.Pipe(handler)
	}
}

// initializeRoutes 应用路由配置和框架内置路由。
// 项目 route 目录对应的加载器由 HTTP 首次路由分发触发，与 ThinkPHP 保持一致。
func (app *App) initializeRoutes(services applicationAssemblyServices) {
	app.applyRouteConfigTo(services.config, services.route)
	if app.operationalRoutesEnabled {
		app.registerOperationalRoutesTo(services.route, services.health, services.metrics)
	}
	app.registerStartupReadinessCheckTo(services.health)
	app.warnProductionSecurity()
}

// LoadRoutes 延迟加载项目路由并触发 RouteLoaded。
// 同一 App 只加载一次，确保并发首请求不会重复注册路由。
func (app *App) LoadRoutes() error {
	if app == nil {
		return ErrNilApplication
	}
	app.routeLoadOnce.Do(func() {
		for _, loader := range app.snapshotRouteRegistry() {
			if err := safeLoadRoutes(loader, app); err != nil {
				app.routeLoadErr = fmt.Errorf("加载应用路由失败: %w", err)
				return
			}
		}
		if err := app.dispatchLifecycleEvent(event.NewRouteLoadedEvent()); err != nil {
			app.routeLoadErr = err
			return
		}
		if err := app.loadRouteNameCache(); err != nil && app.log != nil {
			// 缓存损坏时保留编译路由，保证可重新执行优化命令修复产物。
			app.log.Warning(fmt.Sprintf("命名路由缓存未启用，使用编译路由: %v", err))
		}
	})
	return app.routeLoadErr
}
