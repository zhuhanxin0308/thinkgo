package framework

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

func readDatabaseConfig(connConfig map[string]interface{}) (db.Config, error) {
	allowedFields := map[string]bool{
		"type": true, "hostname": true, "hostport": true, "database": true,
		"username": true, "password": true, "charset": true, "prefix": true,
		"debug": true, "auto_timestamp": true, "create_time_field": true,
		"update_time_field": true, "timestamp_value_type": true,
		"max_open_conns": true, "max_idle_conns": true,
		"conn_max_lifetime_seconds": true, "conn_max_idle_time_seconds": true,
		"params": true, "deploy": true, "rw_separate": true, "master_num": true,
		"slave_no": true, "fields_strict": true, "break_reconnect": true,
		"trigger_sql": true, "fields_cache": true,
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
		{name: "trigger_sql", target: &configuration.TriggerSQL},
		{name: "fields_cache", target: &configuration.FieldsCache},
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
	if err := validateThinkPHPConnectionOptions(connConfig); err != nil {
		return db.Config{}, err
	}
	applyDatabaseFallbacks(&configuration)
	if err := configuration.Validate(); err != nil {
		return db.Config{}, err
	}
	return configuration, nil
}

func validateThinkPHPConnectionOptions(config map[string]interface{}) error {
	for _, field := range []string{"rw_separate", "break_reconnect"} {
		if raw, exists := config[field]; exists {
			value, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("%w: %s 必须是布尔值", db.ErrInvalidDatabaseConfig, field)
			}
			if value {
				return fmt.Errorf("%w: %s=true 需要分布式连接能力，当前连接器未实现", db.ErrInvalidDatabaseConfig, field)
			}
		}
	}
	if raw, exists := config["fields_strict"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return fmt.Errorf("%w: fields_strict 必须是布尔值", db.ErrInvalidDatabaseConfig)
		}
		if !value {
			return fmt.Errorf("%w: fields_strict=false 尚无等价的字段过滤实现", db.ErrInvalidDatabaseConfig)
		}
	}
	if raw, exists := config["deploy"]; exists {
		value, err := readConfigIntValue(raw)
		if err != nil || value != 0 {
			return fmt.Errorf("%w: deploy 当前仅支持 ThinkPHP 默认值 0", db.ErrInvalidDatabaseConfig)
		}
	}
	if raw, exists := config["master_num"]; exists {
		value, err := readConfigIntValue(raw)
		if err != nil || value != 1 {
			return fmt.Errorf("%w: master_num 当前仅支持集中式默认值 1", db.ErrInvalidDatabaseConfig)
		}
	}
	if raw, exists := config["slave_no"]; exists {
		value, ok := raw.(string)
		if !ok || value != "" {
			return fmt.Errorf("%w: slave_no 在集中式连接中必须为空字符串", db.ErrInvalidDatabaseConfig)
		}
	}
	return nil
}

func applyThinkPHPDatabaseDefaults(config map[string]interface{}, target *db.Config) error {
	if target == nil {
		return fmt.Errorf("%w: 数据库连接配置不能为空", db.ErrInvalidDatabaseConfig)
	}
	if rawRules, exists := config["time_query_rule"]; exists {
		rules, ok := rawRules.(map[string]interface{})
		if !ok || len(rules) != 0 {
			return fmt.Errorf("%w: time_query_rule 的 Go 配置当前仅支持默认空对象", db.ErrInvalidDatabaseConfig)
		}
	}
	if rawFormat, exists := config["datetime_format"]; exists {
		format, ok := rawFormat.(string)
		if !ok || format != "Y-m-d H:i:s" {
			return fmt.Errorf("%w: datetime_format 当前仅支持 ThinkPHP 默认值 Y-m-d H:i:s", db.ErrInvalidDatabaseConfig)
		}
	}
	if rawTimestamp, exists := config["auto_timestamp"]; exists {
		switch value := rawTimestamp.(type) {
		case bool:
			target.AutoTimestamp = value
		case string:
			value = strings.ToLower(strings.TrimSpace(value))
			switch value {
			case "int", "timestamp", "datetime", "date":
				target.AutoTimestamp = true
				target.TimestampValueType = value
			default:
				return fmt.Errorf("%w: auto_timestamp 类型 %q 非法", db.ErrInvalidDatabaseConfig, value)
			}
		default:
			return fmt.Errorf("%w: auto_timestamp 必须是布尔值或时间字段类型", db.ErrInvalidDatabaseConfig)
		}
	}
	if rawFields, exists := config["datetime_field"]; exists {
		fields, ok := rawFields.(string)
		if !ok {
			return fmt.Errorf("%w: datetime_field 必须是字符串", db.ErrInvalidDatabaseConfig)
		}
		if fields != "" {
			parts := strings.Split(fields, ",")
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				return fmt.Errorf("%w: datetime_field 必须使用 create_time,update_time 格式", db.ErrInvalidDatabaseConfig)
			}
			target.CreateTimeField = strings.TrimSpace(parts[0])
			target.UpdateTimeField = strings.TrimSpace(parts[1])
		}
	}
	return nil
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
