package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 10
	defaultConnMaxLifetime = 5 * time.Minute
	defaultConnMaxIdleTime = 2 * time.Minute
	connectionPingTimeout  = 5 * time.Second
)

type sqlConnectionPoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

var builtinsOnce sync.Once

// RegisterBuiltins 显式注册框架内置数据库连接器，避免导入 connector 包时通过 init 隐式改变全局注册表。
func RegisterBuiltins() {
	builtinsOnce.Do(func() {
		mustRegisterConnector("mysql", &Mysql{})
		mustRegisterConnector("pgsql", &Pgsql{})
		mustRegisterConnector("sqlsrv", &Sqlsrv{})
		mustRegisterConnector("sqlite", &Sqlite{})
		mustRegisterConnector("mongo", &Mongo{})
		mustRegisterConnector("neo4j", &Neo4j{})
		registerOracle()
	})
}

func resolveSQLConnectionPoolConfig(config db.Config) (sqlConnectionPoolConfig, error) {
	settings := sqlConnectionPoolConfig{
		MaxOpenConns:    defaultMaxOpenConns,
		MaxIdleConns:    defaultMaxIdleConns,
		ConnMaxLifetime: defaultConnMaxLifetime,
		ConnMaxIdleTime: defaultConnMaxIdleTime,
	}
	if config.MaxOpenConns > 0 {
		settings.MaxOpenConns = config.MaxOpenConns
	}
	if config.MaxIdleConns > 0 {
		settings.MaxIdleConns = config.MaxIdleConns
	}
	if settings.MaxIdleConns > settings.MaxOpenConns {
		return sqlConnectionPoolConfig{}, fmt.Errorf("%w: max_idle_conns 不能超过 max_open_conns", db.ErrInvalidDatabaseConfig)
	}
	var err error
	if config.ConnMaxLifetimeSeconds > 0 {
		settings.ConnMaxLifetime, err = secondsDuration(config.ConnMaxLifetimeSeconds)
		if err != nil {
			return sqlConnectionPoolConfig{}, err
		}
	}
	if config.ConnMaxIdleTimeSeconds > 0 {
		settings.ConnMaxIdleTime, err = secondsDuration(config.ConnMaxIdleTimeSeconds)
		if err != nil {
			return sqlConnectionPoolConfig{}, err
		}
	}
	return settings, nil
}

func secondsDuration(seconds int) (time.Duration, error) {
	if seconds < 0 || uint64(seconds) > uint64(math.MaxInt64/int64(time.Second)) {
		return 0, fmt.Errorf("%w: 连接池时长溢出", db.ErrInvalidDatabaseConfig)
	}
	return time.Duration(seconds) * time.Second, nil
}

func applySQLConnectionPool(handle *sql.DB, settings sqlConnectionPoolConfig) {
	handle.SetMaxOpenConns(settings.MaxOpenConns)
	handle.SetMaxIdleConns(settings.MaxIdleConns)
	handle.SetConnMaxLifetime(settings.ConnMaxLifetime)
	handle.SetConnMaxIdleTime(settings.ConnMaxIdleTime)
}

func openSQLConnection(driverName, dataSourceName string, build db.Builder, config db.Config) (db.Connection, error) {
	handle, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}
	return verifySQLConnection(handle, build, config)
}

func openSQLConnectionWithPool(driverName, dataSourceName string, build db.Builder, settings sqlConnectionPoolConfig) (db.Connection, error) {
	handle, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}
	return verifySQLConnectionWithPool(handle, build, settings)
}

func verifySQLConnection(handle *sql.DB, build db.Builder, config db.Config) (db.Connection, error) {
	settings, err := resolveSQLConnectionPoolConfig(config)
	if err != nil {
		if handle != nil {
			_ = handle.Close()
		}
		return nil, err
	}
	return verifySQLConnectionWithPool(handle, build, settings)
}

func verifySQLConnectionWithPool(handle *sql.DB, build db.Builder, settings sqlConnectionPoolConfig) (db.Connection, error) {
	if handle == nil {
		return nil, db.ErrDatabaseUnavailable
	}
	if isNilSQLBuilder(build) {
		return nil, joinCloseError(db.ErrDatabaseUnavailable, handle.Close())
	}
	applySQLConnectionPool(handle, settings)
	ctx, cancel := context.WithTimeout(context.Background(), connectionPingTimeout)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		return nil, joinCloseError(err, handle.Close())
	}
	return db.NewSQLConnection(handle, build), nil
}

// isNilSQLBuilder 识别接口中的类型化 nil，避免连接初始化阶段调用空方言实现。
func isNilSQLBuilder(build db.Builder) bool {
	if build == nil {
		return true
	}
	value := reflect.ValueOf(build)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func validateConnectorConfig(config db.Config, expectedType string, network, requireDatabase bool) (db.Config, error) {
	if config.Type == "" {
		config.Type = expectedType
	}
	if config.Type != expectedType {
		return db.Config{}, fmt.Errorf("%w: 连接器 %s 收到类型 %q", db.ErrInvalidDatabaseConfig, expectedType, config.Type)
	}
	if err := config.Validate(); err != nil {
		return db.Config{}, err
	}
	if requireDatabase && (strings.TrimSpace(config.Database) == "" || strings.ContainsRune(config.Database, 0)) {
		return db.Config{}, fmt.Errorf("%w: database 不能为空", db.ErrInvalidDatabaseConfig)
	}
	if strings.ContainsRune(config.Database, 0) {
		return db.Config{}, fmt.Errorf("%w: database 包含非法字符", db.ErrInvalidDatabaseConfig)
	}
	if network {
		if strings.TrimSpace(config.Hostname) == "" || strings.ContainsAny(config.Hostname, "\x00\r\n\t ") {
			return db.Config{}, fmt.Errorf("%w: hostname 非法", db.ErrInvalidDatabaseConfig)
		}
		if config.Hostport != "" {
			port, err := strconv.Atoi(config.Hostport)
			if err != nil || port < 1 || port > 65535 {
				return db.Config{}, fmt.Errorf("%w: hostport 非法", db.ErrInvalidDatabaseConfig)
			}
		}
	}
	return config, nil
}

func mustRegisterConnector(name string, connector db.Connector) {
	if err := db.RegisterConnector(name, connector); err != nil {
		panic(fmt.Sprintf("注册内置数据库连接器 %s 失败: %v", name, err))
	}
}

func joinCloseError(operationErr, closeErr error) error {
	return errors.Join(operationErr, closeErr)
}
