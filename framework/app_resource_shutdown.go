package framework

import (
	"errors"
	"fmt"

	"thinkgo/framework/cache"
	"thinkgo/framework/db"
	"thinkgo/framework/log"
	"thinkgo/framework/session"
)

// serviceInstanceForShutdown 读取关闭阶段仍然有效的当前服务实例。
// 关闭阶段不能调用 ResolveService，因此这里只做容器级解析，并允许缺少服务。
func serviceInstanceForShutdown(app *App, name ServiceName) interface{} {
	if app == nil || app.container == nil || !app.container.Has(string(name)) {
		return nil
	}
	instance, err := app.container.Make(string(name))
	if err != nil || isNilServiceInstance(instance) {
		return nil
	}
	return instance
}

// appCacheSnapshot 返回应用内部缓存快照，供资源所有权关闭阶段使用。
func appCacheSnapshot(app *App) *cache.Cache {
	if app == nil {
		return nil
	}
	return app.cache
}

// appDBManagerSnapshot 返回应用内部数据库管理器快照，供资源所有权关闭阶段使用。
func appDBManagerSnapshot(app *App) *db.Manager {
	if app == nil {
		return nil
	}
	return app.dbManager
}

// appDBSnapshot 返回应用内部数据库连接快照，供资源所有权关闭阶段使用。
func appDBSnapshot(app *App) *db.DB {
	if app == nil {
		return nil
	}
	return app.db
}

// appLogSnapshot 返回应用内部日志快照，供资源所有权关闭阶段使用。
func appLogSnapshot(app *App) *log.Log {
	if app == nil {
		return nil
	}
	return app.log
}

// appSessionSnapshot 返回 Session Provider 关闭阶段应观察到的当前实例。
func appSessionSnapshot(app *App) *session.Session {
	if app == nil {
		return nil
	}
	return app.session
}

// closeApplicationCaches 关闭去重后的应用缓存实例，并聚合所有关闭错误。
func closeApplicationCaches(resources ...*cache.Cache) error {
	seen := make(map[*cache.Cache]struct{}, len(resources))
	var closeErr error
	for _, resource := range resources {
		if resource == nil {
			continue
		}
		if _, exists := seen[resource]; exists {
			continue
		}
		seen[resource] = struct{}{}
		if err := safeResourceClose("应用缓存", resource.Close); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("关闭应用缓存失败: %w", err))
		}
	}
	return closeErr
}

// closeApplicationDatabases 按管理器优先、独立连接随后关闭数据库资源。
func closeApplicationDatabases(managers []*db.Manager, databases []*db.DB) error {
	seenManagers := make(map[*db.Manager]struct{}, len(managers))
	seenDatabases := make(map[*db.DB]struct{}, len(databases))
	var closeErr error
	for _, manager := range managers {
		if manager == nil {
			continue
		}
		if _, exists := seenManagers[manager]; exists {
			continue
		}
		seenManagers[manager] = struct{}{}
		if err := safeResourceClose("数据库管理器", manager.Close); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("关闭数据库管理器失败: %w", err))
		}
	}
	for _, database := range databases {
		if database == nil {
			continue
		}
		if _, exists := seenDatabases[database]; exists {
			continue
		}
		seenDatabases[database] = struct{}{}
		if err := safeResourceClose("数据库连接", database.Close); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("关闭数据库连接失败: %w", err))
		}
	}
	return closeErr
}

// closeApplicationLogs 关闭去重后的应用日志实例，并保留 panic 转换语义。
func closeApplicationLogs(resources ...*log.Log) error {
	seen := make(map[*log.Log]struct{}, len(resources))
	var closeErr error
	for _, resource := range resources {
		if resource == nil {
			continue
		}
		if _, exists := seen[resource]; exists {
			continue
		}
		seen[resource] = struct{}{}
		if err := safeResourceClose("应用日志", resource.Close); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("关闭应用日志失败: %w", err))
		}
	}
	return closeErr
}
