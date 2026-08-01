package framework

import (
	"errors"
	"fmt"

	"thinkgo/framework/db"
)

// appDatabaseProvider 负责在配置完成后创建数据库管理器并装配数据库连接。
type appDatabaseProvider struct {
	ownedManager *db.Manager
	ownedDB      *db.DB
}

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

	app.dbManager = db.NewManager(defaultConnection)
	provider.ownedManager = app.dbManager
	app.Instance(serviceKeyDBManager, app.dbManager)

	// 控制台命令可跳过数据库连接（version/list/make:* 等不依赖数据库）。
	if databaseConfigured && !app.skipDatabaseInit {
		if databaseConfigErr != nil {
			app.setDatabaseReadinessError(databaseConfigErr)
		} else {
			app.initDatabaseConnections(databaseConfig, defaultConnection)
		}
		app.registerDatabaseReadinessCheck()
	}

	if databaseConfigErr != nil {
		return fmt.Errorf("初始化数据库配置失败: %w", databaseConfigErr)
	}
	if app.db != nil {
		provider.ownedDB = app.db
		app.Instance(serviceKeyDB, app.db)
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
	var currentManager *db.Manager
	if instance := serviceInstanceForShutdown(app, ServiceDBManager); instance != nil {
		currentManager, _ = instance.(*db.Manager)
	}
	var currentDB *db.DB
	if instance := serviceInstanceForShutdown(app, ServiceDB); instance != nil {
		currentDB, _ = instance.(*db.DB)
	}
	return closeApplicationDatabases(
		[]*db.Manager{provider.ownedManager, appDBManagerSnapshot(app), currentManager},
		[]*db.DB{provider.ownedDB, appDBSnapshot(app), currentDB},
	)
}
