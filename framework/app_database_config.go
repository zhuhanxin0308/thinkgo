package framework

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"thinkgo/framework/db"
)

// applyDatabaseEnvOverrides 用环境变量覆盖数据库配置，避免部署环境必须改配置文件。
func applyDatabaseEnvOverrides(app *App, dbConfig *db.Config) error {
	if app == nil || dbConfig == nil {
		return fmt.Errorf("%w: 应用或数据库配置为空", db.ErrInvalidDatabaseConfig)
	}
	working := *dbConfig

	if val := app.env.Get("DB_TYPE"); val != "" {
		working.Type = val
	}
	if val := app.env.Get("DB_HOST"); val != "" {
		working.Hostname = val
	}
	if val := app.env.Get("DB_PORT"); val != "" {
		working.Hostport = val
	}
	if val := app.env.Get("DB_USER"); val != "" {
		working.Username = val
	}
	if val, ok := app.env.Lookup("DB_PASS"); ok {
		working.Password = val
	}
	if val := app.env.Get("DB_NAME"); val != "" {
		working.Database = val
	}
	integerOverrides := []struct {
		name   string
		target *int
	}{
		{name: "DB_MAX_OPEN_CONNS", target: &working.MaxOpenConns},
		{name: "DB_MAX_IDLE_CONNS", target: &working.MaxIdleConns},
		{name: "DB_CONN_MAX_LIFETIME_SECONDS", target: &working.ConnMaxLifetimeSeconds},
		{name: "DB_CONN_MAX_IDLE_TIME_SECONDS", target: &working.ConnMaxIdleTimeSeconds},
	}
	for _, override := range integerOverrides {
		if raw := app.env.Get(override.name); raw != "" {
			value, err := readEnvNonnegativeInt(override.name, raw)
			if err != nil {
				return err
			}
			*override.target = value
		}
	}
	if val := app.env.Get("DB_TIMESTAMP_VALUE_TYPE"); val != "" {
		working.TimestampValueType = val
	}
	*dbConfig = working
	return nil
}

func readDatabaseConfig(connConfig map[string]interface{}) (db.Config, error) {
	allowedFields := map[string]bool{
		"type": true, "hostname": true, "hostport": true, "database": true,
		"username": true, "password": true, "charset": true, "prefix": true,
		"debug": true, "auto_timestamp": true, "create_time_field": true,
		"update_time_field": true, "timestamp_value_type": true,
		"max_open_conns": true, "max_idle_conns": true,
		"conn_max_lifetime_seconds": true, "conn_max_idle_time_seconds": true,
		"params": true,
	}
	keys := make([]string, 0, len(connConfig))
	for key := range connConfig {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowedFields[key] {
			return db.Config{}, fmt.Errorf("%w: 未知数据库配置字段 %q", db.ErrInvalidDatabaseConfig, key)
		}
	}

	configuration := db.Config{}
	stringFields := []struct {
		name   string
		target *string
	}{
		{name: "type", target: &configuration.Type},
		{name: "hostname", target: &configuration.Hostname},
		{name: "hostport", target: &configuration.Hostport},
		{name: "database", target: &configuration.Database},
		{name: "username", target: &configuration.Username},
		{name: "password", target: &configuration.Password},
		{name: "charset", target: &configuration.Charset},
		{name: "prefix", target: &configuration.Prefix},
		{name: "create_time_field", target: &configuration.CreateTimeField},
		{name: "update_time_field", target: &configuration.UpdateTimeField},
		{name: "timestamp_value_type", target: &configuration.TimestampValueType},
	}
	for _, field := range stringFields {
		value, exists := connConfig[field.name]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return db.Config{}, fmt.Errorf("%w: %s 必须是字符串", db.ErrInvalidDatabaseConfig, field.name)
		}
		*field.target = text
	}
	booleanFields := []struct {
		name   string
		target *bool
	}{
		{name: "debug", target: &configuration.Debug},
		{name: "auto_timestamp", target: &configuration.AutoTimestamp},
	}
	for _, field := range booleanFields {
		value, exists := connConfig[field.name]
		if !exists {
			continue
		}
		boolean, ok := value.(bool)
		if !ok {
			return db.Config{}, fmt.Errorf("%w: %s 必须是布尔值", db.ErrInvalidDatabaseConfig, field.name)
		}
		*field.target = boolean
	}
	integerFields := []struct {
		name   string
		target *int
	}{
		{name: "max_open_conns", target: &configuration.MaxOpenConns},
		{name: "max_idle_conns", target: &configuration.MaxIdleConns},
		{name: "conn_max_lifetime_seconds", target: &configuration.ConnMaxLifetimeSeconds},
		{name: "conn_max_idle_time_seconds", target: &configuration.ConnMaxIdleTimeSeconds},
	}
	for _, field := range integerFields {
		value, exists := connConfig[field.name]
		if !exists {
			continue
		}
		parsed, err := readConfigIntValue(value)
		if err != nil {
			return db.Config{}, fmt.Errorf("%w: %s: %w", db.ErrInvalidDatabaseConfig, field.name, err)
		}
		*field.target = parsed
	}
	params, err := readDatabaseParams(connConfig["params"])
	if err != nil {
		return db.Config{}, err
	}
	configuration.Params = params
	applyDatabaseFallbacks(&configuration)
	if err := configuration.Validate(); err != nil {
		return db.Config{}, err
	}
	return configuration, nil
}

// readDatabaseParams 要求 JSON 连接参数显式使用字符串，避免布尔值或浮点数被隐式改写。
func readDatabaseParams(raw interface{}) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	rawParams, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: params 必须是对象", db.ErrInvalidDatabaseConfig)
	}
	if len(rawParams) == 0 {
		return nil, nil
	}

	params := make(map[string]string, len(rawParams))
	keys := make([]string, 0, len(rawParams))
	for key := range rawParams {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%w: params 包含空键", db.ErrInvalidDatabaseConfig)
		}
		value, ok := rawParams[key].(string)
		if !ok {
			return nil, fmt.Errorf("%w: params.%s 必须是字符串", db.ErrInvalidDatabaseConfig, key)
		}
		params[key] = value
	}
	return params, nil
}

func applyDatabaseFallbacks(dbConfig *db.Config) {
	if dbConfig.TimestampValueType == "" {
		dbConfig.TimestampValueType = db.TimestampValueTypeUnix
	}
}

// readConfigIntValue 读取 JSON 非负整数；浮点表示超出精确范围时拒绝。
func readConfigIntValue(raw interface{}) (int, error) {
	var value uint64
	switch typed := raw.(type) {
	case int:
		if typed < 0 {
			return 0, fmt.Errorf("不能为负数")
		}
		value = uint64(typed)
	case int64:
		if typed < 0 {
			return 0, fmt.Errorf("不能为负数")
		}
		value = uint64(typed)
	case uint:
		value = uint64(typed)
	case uint64:
		value = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || math.Trunc(typed) != typed || typed > 1<<53 {
			return 0, fmt.Errorf("必须是可精确表示的非负整数")
		}
		value = uint64(typed)
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("必须是非负整数: %w", err)
		}
		value = parsed
	default:
		return 0, fmt.Errorf("类型 %T 非法", raw)
	}
	converted, err := strconv.Atoi(strconv.FormatUint(value, 10))
	if err != nil {
		return 0, fmt.Errorf("超出当前平台 int 范围")
	}
	return converted, nil
}

func readEnvNonnegativeInt(name, raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: %s 必须是非负整数", db.ErrInvalidDatabaseConfig, name)
	}
	return value, nil
}
