package db

import (
	"fmt"
	"sort"
	"strings"
)

// Connector 数据库连接器接口。
type Connector interface {
	Connect(config Config) (Connection, error)
}

// Config 数据库连接配置。
type Config struct {
	Type                   string            // 数据库类型（mysql/postgres/sqlsrv 等）
	Hostname               string            // 主机地址
	Hostport               string            // 端口号
	Username               string            // 用户名
	Password               string            // 密码
	Database               string            // 数据库名
	Params                 map[string]string // 额外连接参数
	Charset                string            // 字符集
	Prefix                 string            // 表前缀
	MaxOpenConns           int               // 最大打开连接数
	MaxIdleConns           int               // 最大空闲连接数
	ConnMaxLifetimeSeconds int               // 连接最大生命周期（秒）
	ConnMaxIdleTimeSeconds int               // 连接最大空闲时长（秒）
	Debug                  bool              // 是否开启数据库调试诊断
	TriggerSQL             bool              // 是否记录脱敏后的 SQL 操作诊断
	FieldsCache            bool              // 是否加载发布前生成的字段结构缓存
	AutoTimestamp          bool              // 是否自动维护时间戳
	CreateTimeField        string            // 创建时间字段名
	UpdateTimeField        string            // 更新时间字段名
	TimestampValueType     string            // 自动时间戳落库值类型（unix/datetime/timestamp/date/native）
}

// Validate 在连接器接触配置前校验跨驱动通用约束。
func (config Config) Validate() error {
	if !connectorNamePattern.MatchString(config.Type) {
		return fmt.Errorf("%w: 非法数据库类型 %q", ErrInvalidDatabaseConfig, config.Type)
	}
	if config.Prefix != "" {
		if err := validateIdentifier(config.Prefix + "table"); err != nil {
			return fmt.Errorf("%w: 非法表前缀: %w", ErrInvalidDatabaseConfig, err)
		}
	}
	fields := []struct{ name, value string }{
		{name: "create_time_field", value: config.CreateTimeField},
		{name: "update_time_field", value: config.UpdateTimeField},
	}
	for _, field := range fields {
		if field.value != "" {
			if err := validateIdentifier(field.value); err != nil {
				return fmt.Errorf("%w: %s 非法: %w", ErrInvalidDatabaseConfig, field.name, err)
			}
		}
	}
	if config.TimestampValueType != "" {
		if !isSupportedTimestampValueType(config.TimestampValueType) {
			return fmt.Errorf("%w: 非法时间戳类型 %q", ErrInvalidDatabaseConfig, config.TimestampValueType)
		}
	}
	if config.MaxOpenConns < 0 || config.MaxIdleConns < 0 || config.ConnMaxLifetimeSeconds < 0 || config.ConnMaxIdleTimeSeconds < 0 {
		return fmt.Errorf("%w: 连接池数量和时长不能为负数", ErrInvalidDatabaseConfig)
	}
	if config.MaxOpenConns > 0 && config.MaxIdleConns > config.MaxOpenConns {
		return fmt.Errorf("%w: max_idle_conns 不能超过 max_open_conns", ErrInvalidDatabaseConfig)
	}
	paramKeys := make([]string, 0, len(config.Params))
	for key := range config.Params {
		paramKeys = append(paramKeys, key)
	}
	sort.Strings(paramKeys)
	for _, key := range paramKeys {
		value := config.Params[key]
		if strings.TrimSpace(key) != key || key == "" || len(key) > maxIdentifierPartLength || strings.ContainsRune(key, 0) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: 非法连接参数键 %q", ErrInvalidDatabaseConfig, key)
		}
	}
	return nil
}

func (config Config) clone() Config {
	cloned := config
	if config.Params != nil {
		cloned.Params = make(map[string]string, len(config.Params))
		for key, value := range config.Params {
			cloned.Params[key] = value
		}
	}
	return cloned
}
