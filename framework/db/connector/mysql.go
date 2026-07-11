package connector

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"sort"
	"time"

	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

// 连接池默认参数。
const (
	defaultMaxOpenConns    = 25              // 最大打开连接数
	defaultMaxIdleConns    = 10              // 最大空闲连接数
	defaultConnMaxLifetime = 5 * time.Minute // 连接最大生命周期
	defaultConnMaxIdleTime = 2 * time.Minute // 连接最大空闲时长
)

// Mysql MySQL 连接器。
type Mysql struct{}

// mysqlConnectionPoolConfig 描述最终生效的连接池参数。
type mysqlConnectionPoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Connect 连接到 MySQL 数据库。
func (m *Mysql) Connect(config db.Config) (db.Connection, error) {
	dsn := buildMysqlDSN(config)
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}

	// 连接池参数允许按环境调优，避免不同机器与流量档位共用硬编码值。
	poolConfig := resolveMysqlConnectionPoolConfig(config)
	conn.SetMaxOpenConns(poolConfig.MaxOpenConns)
	conn.SetMaxIdleConns(poolConfig.MaxIdleConns)
	conn.SetConnMaxLifetime(poolConfig.ConnMaxLifetime)
	conn.SetConnMaxIdleTime(poolConfig.ConnMaxIdleTime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &db.SQLConnection{DB: conn, Builder: &builder.Mysql{}}, nil
}

// resolveMysqlConnectionPoolConfig 计算最终生效的连接池配置，非法值回退到默认参数。
func resolveMysqlConnectionPoolConfig(config db.Config) mysqlConnectionPoolConfig {
	settings := mysqlConnectionPoolConfig{
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
	if config.ConnMaxLifetimeSeconds > 0 {
		settings.ConnMaxLifetime = time.Duration(config.ConnMaxLifetimeSeconds) * time.Second
	}
	if config.ConnMaxIdleTimeSeconds > 0 {
		settings.ConnMaxIdleTime = time.Duration(config.ConnMaxIdleTimeSeconds) * time.Second
	}
	return settings
}

// buildMysqlDSN 统一构建 MySQL DSN，并注入连接存活检查相关参数。
func buildMysqlDSN(config db.Config) string {
	charset := config.Charset
	if charset == "" {
		charset = "utf8mb4"
	}

	params := map[string]string{
		"charset":           charset,
		"checkConnLiveness": "true",
		"loc":               "Local",
		"parseTime":         "True",
	}
	for key, value := range config.Params {
		params[key] = value
	}

	query := url.Values{}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		query.Set(key, params[key])
	}

	mysqlConfig := mysqlDriver.Config{
		User:   config.Username,
		Passwd: config.Password,
		Net:    "tcp",
		Addr:   joinHostPort(config.Hostname, config.Hostport),
		DBName: config.Database,
		Params: map[string]string{},
	}
	for key, values := range query {
		if len(values) > 0 {
			// 统一保留字符串参数，交给官方驱动负责 DSN 转义。
			mysqlConfig.Params[key] = values[0]
		}
	}

	return mysqlConfig.FormatDSN()
}

// joinHostPort 在端口存在时使用标准库组合地址，避免 IPv6 或特殊主机名被拼错。
func joinHostPort(host string, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func init() {
	db.RegisterConnector("mysql", &Mysql{})
}
