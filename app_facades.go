package framework

import (
	"github.com/zhuhanxin0308/thinkgo/framework/cache"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/cookie"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/debug"
	"github.com/zhuhanxin0308/thinkgo/framework/env"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
	"github.com/zhuhanxin0308/thinkgo/framework/filesystem"
	"github.com/zhuhanxin0308/thinkgo/framework/health"
	"github.com/zhuhanxin0308/thinkgo/framework/lang"
	"github.com/zhuhanxin0308/thinkgo/framework/log"
	"github.com/zhuhanxin0308/thinkgo/framework/metrics"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
	"github.com/zhuhanxin0308/thinkgo/framework/migration"
	"github.com/zhuhanxin0308/thinkgo/framework/session"
	"github.com/zhuhanxin0308/thinkgo/framework/telemetry"
	"github.com/zhuhanxin0308/thinkgo/framework/view"
)

// Container 返回当前应用容器。
// 对应 ThinkPHP App 本身提供的容器能力，业务代码无需通过字符串服务名取回容器。
func (app *App) Container() *Container {
	if app == nil || app.container == nil {
		return nil
	}
	// 手工构造的 App 也必须在首次暴露容器时建立归属，避免公开指针绕过生命周期门禁。
	if err := app.container.attachApplication(app); err != nil {
		return nil
	}
	return app.container
}

// Config 返回应用配置服务。
func (app *App) Config() *config.Config {
	if app == nil {
		return nil
	}
	return app.config
}

// Env 返回环境变量服务。
func (app *App) Env() *env.Env {
	if app == nil {
		return nil
	}
	return app.env
}

// Event 返回事件调度服务。
func (app *App) Event() *event.Dispatcher {
	if app == nil {
		return nil
	}
	return app.event
}

// Route 返回路由服务。
func (app *App) Route() *Route {
	if app == nil {
		return nil
	}
	return app.routeFacade
}

// Middleware 返回全局中间件服务。
func (app *App) Middleware() *middleware.Pipeline {
	if app == nil {
		return nil
	}
	return app.middleware
}

// Log 返回日志服务；依赖配置的驱动在应用初始化后可用。
func (app *App) Log() *log.Log {
	if app == nil {
		return nil
	}
	return app.log
}

// Lang 返回语言服务。
func (app *App) Lang() *lang.Lang {
	if app == nil {
		return nil
	}
	return app.lang
}

// Cache 返回缓存服务；未完成初始化时可能为空。
func (app *App) Cache() *cache.Cache {
	if app == nil {
		return nil
	}
	return app.cache
}

// Filesystem 返回文件系统管理器；业务代码可直接调用 Disk，无需理解驱动装配。
func (app *App) Filesystem() *filesystem.Filesystem {
	if app == nil {
		return nil
	}
	return app.filesystem
}

// Cookie 返回 Cookie 服务；未完成初始化时可能为空。
func (app *App) Cookie() *cookie.Cookie {
	if app == nil {
		return nil
	}
	return app.cookie
}

// Session 返回 Session 服务；未启用 Session 时可能为空。
func (app *App) Session() *session.Session {
	if app == nil {
		return nil
	}
	return app.session
}

// DB 返回默认数据库连接；lazy 策略会在这里完成首次连接。
func (app *App) DB() *db.DB {
	if app == nil {
		return nil
	}
	if app.db != nil {
		return app.db
	}
	if app.dbManager == nil {
		return nil
	}
	database, err := app.dbManager.Default()
	if err != nil {
		return nil
	}
	return database
}

// DBManager 返回数据库连接管理器；应用初始化后可用。
func (app *App) DBManager() *db.Manager {
	if app == nil {
		return nil
	}
	return app.dbManager
}

// View 返回视图服务；应用初始化后可用。
func (app *App) View() *view.View {
	if app == nil {
		return nil
	}
	return app.view
}

// DebugManager 返回底层调试记录服务，避免与 ThinkPHP App.Debug 开关重名。
func (app *App) DebugManager() *debug.Debug {
	if app == nil {
		return nil
	}
	return app.debug
}

// Metrics 返回指标服务。
func (app *App) Metrics() *metrics.Registry {
	if app == nil {
		return nil
	}
	return app.metrics
}

// Health 返回健康检查服务。
func (app *App) Health() *health.Registry {
	if app == nil {
		return nil
	}
	return app.health
}

// Migration 返回迁移注册表。
func (app *App) Migration() *migration.Registry {
	if app == nil {
		return nil
	}
	return app.migrations
}

// Telemetry 返回链路追踪服务。
func (app *App) Telemetry() *telemetry.Tracing {
	if app == nil {
		return nil
	}
	return app.telemetry
}
