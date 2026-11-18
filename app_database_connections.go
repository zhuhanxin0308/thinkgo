package framework

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

// defaultDatabaseConnectionName 是未配置数据库时空管理器使用的稳定内部默认名称。
const defaultDatabaseConnectionName = "default"

const maxDatabaseInitializationConcurrency = 4

type databaseConnectionSpec struct {
	name   string
	config db.Config
}

type databaseConnectionResult struct {
	name     string
	database *db.DB
	err      error
}

// initDatabaseConnections 按配置并行建立数据库连接，并把默认连接绑定到 ServiceDB。
// 并发数固定有界，结果在安装前按连接名称排序，避免启动顺序和默认连接语义漂移。
func (app *App) initDatabaseConnections(dbConfigData map[string]interface{}, defaultConn string) {
	specs := app.databaseConnectionSpecs(dbConfigData, defaultConn)

	results := initializeDatabaseConnections(specs)
	for _, spec := range specs {
		result := results[spec.name]
		if result.err != nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(result.err)
				if app.databaseStartupPolicy == DatabaseStartupRequired {
					app.recordStartupError(fmt.Errorf("required default database connection %q failed: %w", spec.name, result.err))
				}
			}
			if app.log != nil && app.log.IsLevelEnabled(log.LevelWarning) {
				connectionError := redactDatabaseConnectionError(result.err)
				message := fmt.Sprintf("数据库连接 %q 失败（应用仍可启动，但 readiness 将失败，启动策略=%s）: %s", spec.name, app.databaseStartupPolicy, connectionError)
				if app.databaseStartupPolicy == DatabaseStartupRequired && spec.name == defaultConn {
					message = fmt.Sprintf("数据库连接 %q 失败（required 策略将阻断启动）: %s", spec.name, connectionError)
				}
				app.log.Warning(message)
			}
			if app.databaseStartupPolicy != DatabaseStartupRequired || spec.name != defaultConn {
				if retryErr := app.registerDatabaseConnectionFactory(spec, defaultConn); retryErr != nil {
					app.recordStartupError(retryErr)
				}
			}
			continue
		}
		database := result.database
		if database == nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(db.ErrDatabaseUnavailable)
				if app.databaseStartupPolicy == DatabaseStartupRequired {
					app.recordStartupError(fmt.Errorf("required default database connection %q unavailable: %w", spec.name, db.ErrDatabaseUnavailable))
				}
			}
			if app.log != nil {
				app.log.Warningf("数据库连接 %q 未返回可用连接（启动策略=%s）", spec.name, app.databaseStartupPolicy)
			}
			continue
		}

		database.SetLocation(app.Location())
		database.SetLogger(app.log)
		app.loadDatabaseSchemaCache(spec, database)
		if err := app.dbManager.Add(spec.name, database); err != nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			closeErr := database.Close()
			app.recordStartupError(errors.Join(fmt.Errorf("database connection %q registration failed: %w", spec.name, err), closeErr))
			continue
		}
		if spec.name == defaultConn {
			app.db = database
			app.setDatabaseReadinessError(nil)
		}
	}
}

// databaseConnectionSpecs 在任何网络连接前完整校验命名连接配置。
func (app *App) databaseConnectionSpecs(dbConfigData map[string]interface{}, defaultConn string) []databaseConnectionSpec {
	rawConnections, exists := dbConfigData["connections"]
	connections, ok := rawConnections.(map[string]interface{})
	if !exists || !ok || len(connections) == 0 {
		app.setDatabaseReadinessError(fmt.Errorf("%w: database.connections 必须是非空对象", db.ErrInvalidDatabaseConfig))
		app.recordStartupError(fmt.Errorf("%w: database.connections 必须是非空对象", db.ErrInvalidDatabaseConfig))
		return nil
	}
	if _, exists := connections[defaultConn]; !exists {
		app.setDatabaseReadinessError(fmt.Errorf("%w: 默认连接 %q 未在 database.connections 中声明", db.ErrInvalidDatabaseConfig, defaultConn))
		app.recordStartupError(fmt.Errorf("%w: 默认连接 %q 未在 database.connections 中声明", db.ErrInvalidDatabaseConfig, defaultConn))
	}

	names := make([]string, 0, len(connections))
	for name := range connections {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]databaseConnectionSpec, 0, len(names))
	production := app.securityWarningEnvironment() == securityEnvironmentProduction
	for _, name := range names {
		if err := db.ValidateConnectionName(name); err != nil {
			if name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			app.recordStartupError(fmt.Errorf("database connection %q invalid: %w", name, err))
			continue
		}
		connConfig, ok := connections[name].(map[string]interface{})
		if !ok {
			if name == defaultConn {
				app.setDatabaseReadinessError(db.ErrInvalidDatabaseConfig)
			}
			app.recordStartupError(fmt.Errorf("%w: 数据库连接 %q 配置必须是对象", db.ErrInvalidDatabaseConfig, name))
			continue
		}

		databaseConfig, err := readDatabaseConfig(connConfig)
		if err == nil {
			err = applyThinkPHPDatabaseDefaults(dbConfigData, &databaseConfig)
		}
		if err == nil && production && strings.EqualFold(strings.TrimSpace(databaseConfig.Type), "mongo") {
			err = validateMongoProductionTLS(databaseConfig.Params)
		}
		if err != nil {
			if name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			app.recordStartupError(fmt.Errorf("database connection %q invalid: %w", name, err))
			continue
		}
		applyDatabaseFallbacks(&databaseConfig)
		specs = append(specs, databaseConnectionSpec{name: name, config: databaseConfig})
	}
	return specs
}

// registerLazyDatabaseConnections 注册通过配置校验的连接工厂，默认连接首次访问后
// 才会真正拨号，保持 ThinkPHP 数据库管理器的按需连接行为。
func (app *App) registerLazyDatabaseConnections(dbConfigData map[string]interface{}, defaultConn string) bool {
	specs := app.databaseConnectionSpecs(dbConfigData, defaultConn)
	defaultRegistered := false
	for _, spec := range specs {
		current := spec
		err := app.registerDatabaseConnectionFactory(current, defaultConn)
		if err != nil {
			if current.name == defaultConn {
				app.setDatabaseReadinessError(err)
			}
			app.recordStartupError(fmt.Errorf("注册惰性数据库连接 %q 失败: %w", current.name, err))
			continue
		}
		if current.name == defaultConn {
			defaultRegistered = true
		}
	}
	return defaultRegistered
}

// registerDatabaseConnectionFactory 让首次连接故障保留可重试入口，支持无业务流量时由就绪探针恢复。
func (app *App) registerDatabaseConnectionFactory(spec databaseConnectionSpec, defaultConn string) error {
	return app.dbManager.RegisterFactory(spec.name, func() (*db.DB, error) {
		database, connectErr := connectDatabaseSafely(spec.config)
		if connectErr != nil {
			if spec.name == defaultConn {
				app.setDatabaseReadinessError(connectErr)
			}
			if app.log != nil && app.log.IsLevelEnabled(log.LevelWarning) {
				app.log.Warningf("数据库连接 %q 失败（%s 策略将在下次访问或就绪检查时重试）: %s", spec.name, app.databaseStartupPolicy, redactDatabaseConnectionError(connectErr))
			}
			return nil, connectErr
		}
		database.SetLocation(app.Location())
		database.SetLogger(app.log)
		app.loadDatabaseSchemaCache(spec, database)
		if spec.name == defaultConn {
			app.setDatabaseReadinessError(nil)
		}
		return database, nil
	})
}

// initializeDatabaseConnections 使用固定数量 worker，避免多个远程数据库的 Ping 让启动时间线性增长。
func initializeDatabaseConnections(specs []databaseConnectionSpec) map[string]databaseConnectionResult {
	results := make(map[string]databaseConnectionResult, len(specs))
	if len(specs) == 0 {
		return results
	}
	workerCount := len(specs)
	if workerCount > maxDatabaseInitializationConcurrency {
		workerCount = maxDatabaseInitializationConcurrency
	}
	jobs := make(chan databaseConnectionSpec)
	resultChannel := make(chan databaseConnectionResult, len(specs))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for index := 0; index < workerCount; index++ {
		go func() {
			defer workers.Done()
			for spec := range jobs {
				database, err := connectDatabaseSafely(spec.config)
				resultChannel <- databaseConnectionResult{name: spec.name, database: database, err: err}
			}
		}()
	}
	go func() {
		for _, spec := range specs {
			jobs <- spec
		}
		close(jobs)
		workers.Wait()
		close(resultChannel)
	}()
	for result := range resultChannel {
		results[result.name] = result
	}
	return results
}

// connectDatabaseSafely 将第三方连接器 panic 收敛为连接失败，避免后台 worker 直接崩溃宿主进程。
func connectDatabaseSafely(config db.Config) (database *db.DB, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			database = nil
			err = fmt.Errorf("数据库连接器 panic: %v", recovered)
		}
	}()
	return db.Connect(config)
}

func readDefaultDatabaseConnection(config map[string]interface{}) (string, error) {
	if len(config) == 0 {
		return "", fmt.Errorf("%w: database 配置不能为空", db.ErrInvalidDatabaseConfig)
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "default" && key != "connections" && key != "time_query_rule" &&
			key != "auto_timestamp" && key != "datetime_format" && key != "datetime_field" {
			return "", fmt.Errorf("%w: database 包含未知字段 %q", db.ErrInvalidDatabaseConfig, key)
		}
	}
	value, exists := config["default"]
	defaultConnection, ok := value.(string)
	if !exists || !ok || strings.TrimSpace(defaultConnection) != defaultConnection || defaultConnection == "" {
		return "", fmt.Errorf("%w: database.default 必须是非空字符串", db.ErrInvalidDatabaseConfig)
	}
	if err := db.ValidateConnectionName(defaultConnection); err != nil {
		return "", err
	}
	return defaultConnection, nil
}
