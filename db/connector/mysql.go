package connector

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

// Mysql MySQL 连接器。
type Mysql struct{}

type mysqlConnectionPoolConfig = sqlConnectionPoolConfig

var mysqlParameterKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Connect 连接到 MySQL 数据库。
func (m *Mysql) Connect(config db.Config) (db.Connection, error) {
	validated, err := validateConnectorConfig(config, "mysql", true, true)
	if err != nil {
		return nil, err
	}
	mysqlConfig, err := buildMysqlConfig(validated)
	if err != nil {
		return nil, err
	}
	return openSQLConnection("mysql", mysqlConfig.FormatDSN(), builder.NewMysql(mysqlConfig.MaxAllowedPacket), validated)
}

// resolveMysqlConnectionPoolConfig 计算最终生效的连接池配置，非法值回退到默认参数。
func resolveMysqlConnectionPoolConfig(config db.Config) mysqlConnectionPoolConfig {
	settings, _ := resolveSQLConnectionPoolConfig(config)
	return settings
}

// validateMysqlParameters 保留旧版参数校验入口，并复用 typed Config 的完整校验逻辑。
func validateMysqlParameters(parameters map[string]string) error {
	_, err := buildMysqlConfig(db.Config{Params: parameters})
	return err
}

func buildMysqlConfig(config db.Config) (*mysqlDriver.Config, error) {
	if strings.Contains(config.Username, ":") {
		return nil, fmt.Errorf("%w: MySQL 用户名不能包含冒号", db.ErrInvalidDatabaseConfig)
	}
	charset := config.Charset
	if charset == "" {
		charset = "utf8mb4"
	}
	mysqlConfig := mysqlDriver.NewConfig()
	mysqlConfig.User = config.Username
	mysqlConfig.Passwd = config.Password
	mysqlConfig.Net = "tcp"
	mysqlConfig.Addr = joinHostPort(config.Hostname, config.Hostport)
	mysqlConfig.DBName = config.Database
	mysqlConfig.CheckConnLiveness = true
	mysqlConfig.ParseTime = true
	mysqlConfig.Loc = time.UTC
	mysqlConfig.TLSConfig = "true"
	mysqlConfig.Params = make(map[string]string)
	if err := mysqlConfig.Apply(mysqlDriver.Charset(charset, "")); err != nil {
		return nil, fmt.Errorf("%w: MySQL 字符集非法: %v", db.ErrInvalidDatabaseConfig, err)
	}

	seen := make(map[string]string, len(config.Params))
	for key, value := range config.Params {
		canonical := strings.ToLower(strings.TrimSpace(key))
		if !mysqlParameterKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("%w: MySQL 参数键 %q 非法", db.ErrInvalidDatabaseConfig, key)
		}
		if previous, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("%w: MySQL 参数 %q 与 %q 重复", db.ErrInvalidDatabaseConfig, previous, key)
		}
		seen[canonical] = key
		if isDangerousMysqlBoolean(canonical) {
			enabled, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil || enabled {
				return nil, fmt.Errorf("%w: MySQL 高风险参数 %s 非法", db.ErrInvalidDatabaseConfig, key)
			}
			continue
		}
		if err := applyMysqlTypedParameter(mysqlConfig, canonical, key, value); err != nil {
			return nil, err
		}
	}
	return mysqlConfig, nil
}

func isDangerousMysqlBoolean(canonical string) bool {
	switch canonical {
	case "multistatements", "allowallfiles", "allowcleartextpasswords", "allowfallbacktoplaintext", "allowoldpasswords":
		return true
	default:
		return false
	}
}

func applyMysqlTypedParameter(config *mysqlDriver.Config, canonical, original, value string) error {
	trimmed := strings.TrimSpace(value)
	switch canonical {
	case "tls":
		if trimmed == "" {
			return fmt.Errorf("%w: MySQL tls 参数不能为空", db.ErrInvalidDatabaseConfig)
		}
		if parsed, err := strconv.ParseBool(trimmed); err == nil {
			config.TLSConfig = strconv.FormatBool(parsed)
		} else {
			config.TLSConfig = trimmed
		}
	case "parsetime":
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return fmt.Errorf("%w: MySQL parseTime 参数非法", db.ErrInvalidDatabaseConfig)
		}
		config.ParseTime = parsed
	case "checkconnliveness":
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return fmt.Errorf("%w: MySQL checkConnLiveness 参数非法", db.ErrInvalidDatabaseConfig)
		}
		config.CheckConnLiveness = parsed
	case "clientfoundrows":
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return fmt.Errorf("%w: MySQL clientFoundRows 参数非法", db.ErrInvalidDatabaseConfig)
		}
		config.ClientFoundRows = parsed
	case "maxallowedpacket":
		parsed, err := strconv.Atoi(trimmed)
		if err != nil || parsed < 0 {
			return fmt.Errorf("%w: MySQL maxAllowedPacket 参数非法", db.ErrInvalidDatabaseConfig)
		}
		config.MaxAllowedPacket = parsed
	case "loc":
		if !strings.EqualFold(trimmed, "UTC") {
			return fmt.Errorf("%w: MySQL loc 只能为 UTC", db.ErrInvalidDatabaseConfig)
		}
		config.Loc = time.UTC
	case "time_zone":
		unquoted := strings.Trim(trimmed, "'\"")
		if !strings.EqualFold(unquoted, "UTC") && unquoted != "+00:00" {
			return fmt.Errorf("%w: MySQL time_zone 只能为 UTC", db.ErrInvalidDatabaseConfig)
		}
		config.Params[original] = "'+00:00'"
	case "charset":
		if trimmed == "" {
			return fmt.Errorf("%w: MySQL charset 参数不能为空", db.ErrInvalidDatabaseConfig)
		}
		if err := config.Apply(mysqlDriver.Charset(trimmed, "")); err != nil {
			return fmt.Errorf("%w: MySQL charset 参数非法: %v", db.ErrInvalidDatabaseConfig, err)
		}
	default:
		config.Params[original] = value
	}
	return nil
}

// buildMysqlDSN 使用官方 typed Config 构建 DSN，避免字符串拼接改变参数结构。
func buildMysqlDSN(config db.Config) (string, error) {
	mysqlConfig, err := buildMysqlConfig(config)
	if err != nil {
		return "", err
	}
	return mysqlConfig.FormatDSN(), nil
}

// joinHostPort 在端口存在时使用标准库组合地址，避免 IPv6 或特殊主机名被拼错。
func joinHostPort(host string, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
