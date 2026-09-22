package framework

import (
	"errors"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// appDatabaseProvider 负责在配置完成后创建数据库管理器并装配数据库连接。
type appDatabaseProvider struct{}

// Register 保留 Provider 注册阶段，数据库连接必须等待最终配置和日志完成后创建。
func (provider *appDatabaseProvider) Register(app *App) error {
	return nil
}

// Initialize 初始化数据库管理器、连接池和 readiness 检查。
func (provider *appDatabaseProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}

	databaseConfigured := app.config.Has("database")
	databaseConfig := app.config.GetMap("database")
	defaultConnection := defaultDatabaseConnectionName
	var databaseConfigErr error

	if databaseConfigured {
		configuredDefault, err := readDefaultDatabaseConnection(databaseConfig)
		databaseConfigErr = err
		if databaseConfigErr == nil {
			defaultConnection = configuredDefault
		}
	}

	if err := app.installManagedService(serviceKeyDBManager, db.NewManager(defaultConnection)); err != nil {
		return fmt.Errorf("绑定数据库管理器服务失败: %w", err)
	}
	if !databaseConfigured && !app.skipDatabaseInit {
		missingConfigErr := fmt.Errorf("%w: 未提供 database 配置", db.ErrDatabaseUnavailable)
		switch app.databaseStartupPolicy {
		case DatabaseStartupRequired:
			app.setDatabaseReadinessError(missingConfigErr)
			app.registerDatabaseReadinessCheck()
			return fmt.Errorf("required 数据库启动策略缺少配置: %w", missingConfigErr)
		case DatabaseStartupDegraded:
			// 仅显式 degraded 策略把缺失配置视为未就绪；未声明策略的旧无数据库应用保持兼容。
			if app.config.Has("app.database_startup_policy") {
				app.setDatabaseReadinessError(missingConfigErr)
				app.registerDatabaseReadinessCheck()
			}
		}
	}

	// 控制台命令可跳过数据库连接（version/list/make:* 等不依赖数据库）。
	defaultDatabaseRegistered := false
	if databaseConfigured && !app.skipDatabaseInit && app.databaseStartupPolicy != DatabaseStartupDisabled {
		if databaseConfigErr != nil {
			app.setDatabaseReadinessError(databaseConfigErr)
		} else if app.databaseStartupPolicy == DatabaseStartupLazy {
			defaultDatabaseRegistered = app.registerLazyDatabaseConnections(databaseConfig, defaultConnection)
		} else {
			app.initDatabaseConnections(databaseConfig, defaultConnection)
			defaultDatabaseRegistered = app.databaseStartupPolicy == DatabaseStartupDegraded && app.databaseReadinessError() != nil
		}
		app.registerDatabaseReadinessCheck()
	}

	if databaseConfigErr != nil {
		return fmt.Errorf("初始化数据库配置失败: %w", databaseConfigErr)
	}
	if app.db != nil {
		if err := app.installManagedService(serviceKeyDB, app.db); err != nil {
			return fmt.Errorf("绑定默认数据库服务失败: %w", err)
		}
	} else if defaultDatabaseRegistered {
		if err := app.bindDeferredManagedService(serviceKeyDB, func() (*db.DB, error) {
			return app.dbManager.Default()
		}); err != nil {
			return fmt.Errorf("绑定惰性默认数据库服务失败: %w", err)
		}
	}
	return nil
}

// Boot 不在 Provider 启动阶段重复建立数据库连接。
func (provider *appDatabaseProvider) Boot(app *App) error {
	return nil
}

// Shutdown 先关闭数据库管理器，再关闭未被管理器持有的当前连接。
func (provider *appDatabaseProvider) Shutdown(app *App) error {
	if provider == nil {
		return nil
	}
	managerErr := app.closeServiceResources(serviceKeyDBManager)
	return errors.Join(managerErr, app.closeServiceResources(serviceKeyDB))
}
