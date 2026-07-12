package connector

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"thinkgo/framework/db"
	"thinkgo/framework/db/builder"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

// Mysql MySQL 连接器。
type Mysql struct{}

type mysqlConnectionPoolConfig = sqlConnectionPoolConfig

// Connect 连接到 MySQL 数据库。
func (m *Mysql) Connect(config db.Config) (db.Connection, error) {
	validated, err := validateConnectorConfig(config, "mysql", true, true)
	if err != nil {
		return nil, err
	}
	if err := validateMysqlParameters(validated.Params); err != nil {
		return nil, err
	}
	return openSQLConnection("mysql", buildMysqlDSN(validated), &builder.Mysql{}, validated)
}

// resolveMysqlConnectionPoolConfig 计算最终生效的连接池配置，非法值回退到默认参数。
func resolveMysqlConnectionPoolConfig(config db.Config) mysqlConnectionPoolConfig {
	settings, _ := resolveSQLConnectionPoolConfig(config)
	return settings
}

func validateMysqlParameters(params map[string]string) error {
	for _, key := range []string{"multiStatements", "allowAllFiles", "allowCleartextPasswords", "allowFallbackToPlaintext"} {
		if value, exists := params[key]; exists && strings.EqualFold(strings.TrimSpace(value), "true") {
			return fmt.Errorf("%w: MySQL 参数 %s=true 被禁止", db.ErrInvalidDatabaseConfig, key)
		}
	}
	return nil
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
		"tls":               "true",
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
	mustRegisterConnector("mysql", &Mysql{})
}
