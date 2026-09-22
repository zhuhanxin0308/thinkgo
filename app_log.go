package framework

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/log"
	logDriver "github.com/zhuhanxin0308/thinkgo/v3/log/driver"
)

// createAppLogChannel 严格解析单个日志通道，配置错误在启动阶段立即返回。
func createAppLogChannel(app *App, channelConfig map[string]interface{}, addConsole bool) (*log.Log, error) {
	allowedFields := map[string]struct{}{
		"type": {}, "level": {}, "path": {}, "overflow_policy": {},
		"max_file_size": {}, "retention_days": {}, "max_files": {},
		"max_total_size": {},
		"single":         {}, "apart_level": {}, "json": {}, "processor": {},
		"close": {}, "format": {}, "realtime_write": {},
	}
	unknownFields := make([]string, 0)
	for key := range channelConfig {
		if _, exists := allowedFields[key]; !exists {
			unknownFields = append(unknownFields, key)
		}
	}
	if len(unknownFields) > 0 {
		sort.Strings(unknownFields)
		return nil, fmt.Errorf("日志通道包含未知配置字段: %s", strings.Join(unknownFields, ", "))
	}

	driverType := "file"
	if value, exists := channelConfig["type"]; exists {
		typed, ok := value.(string)
		if !ok || strings.TrimSpace(typed) == "" {
			return nil, invalidLogConfigValueError("type", value)
		}
		driverType = strings.ToLower(strings.TrimSpace(typed))
	}
	if driverType != "file" {
		return nil, fmt.Errorf("不支持的日志驱动类型 %q", driverType)
	}
	if err := validateThinkPHPLogChannelOptions(channelConfig); err != nil {
		return nil, err
	}

	levels, err := readLogLevels(channelConfig["level"])
	if err != nil {
		return nil, err
	}
	overflowPolicy, err := readLogOverflowPolicy(channelConfig)
	if err != nil {
		return nil, err
	}
	path := app.RuntimeLogPath()
	if value, exists := channelConfig["path"]; exists {
		typed, ok := value.(string)
		if !ok {
			return nil, invalidLogConfigValueError("path", value)
		}
		if strings.TrimSpace(typed) != "" {
			path, err = app.resolveStoragePath(strings.TrimSpace(typed))
			if err != nil {
				return nil, fmt.Errorf("解析日志路径失败: %w", err)
			}
		}
	}

	options, err := readLogFileOptions(channelConfig)
	if err != nil {
		return nil, err
	}
	fileDriver, err := logDriver.NewFileWithOptions(path, options)
	if err != nil {
		return nil, err
	}
	logger := log.NewLog(fileDriver)
	logger.SetLocation(app.Location())
	if addConsole {
		if err := logger.AddDriver(logDriver.NewConsole()); err != nil {
			_ = logger.Close()
			return nil, fmt.Errorf("添加控制台日志驱动失败: %w", err)
		}
		logger.SetCallerEnabled(true)
	}
	logger.SetLevels(levels)
	if err := logger.SetOverflowPolicy(overflowPolicy); err != nil {
		_ = logger.Close()
		return nil, fmt.Errorf("设置日志溢出策略失败: %w", err)
	}
	return logger, nil
}

func validateThinkPHPLogChannelOptions(config map[string]interface{}) error {
	for _, name := range []string{"single", "json", "close", "realtime_write"} {
		if raw, exists := config[name]; exists {
			value, ok := raw.(bool)
			if !ok {
				return invalidLogConfigValueError(name, raw)
			}
			if value {
				return fmt.Errorf("日志配置 %s=true 尚无等价的文件驱动实现", name)
			}
		}
	}
	if raw, exists := config["apart_level"]; exists {
		levels, err := readLogLevels(raw)
		if err != nil {
			return err
		}
		if len(levels) > 0 {
			return fmt.Errorf("日志配置 apart_level 尚无等价的分级文件实现")
		}
	}
	if raw, exists := config["processor"]; exists && raw != nil {
		return fmt.Errorf("日志配置 processor 必须在 Go 服务代码中注册，JSON 默认值只能为 null")
	}
	if raw, exists := config["format"]; exists {
		format, ok := raw.(string)
		if !ok || format == "" {
			return invalidLogConfigValueError("format", raw)
		}
		if format != "[%s][%s] %s" {
			return fmt.Errorf("日志配置 format %q 尚无等价的文件驱动实现", format)
		}
	}
	return nil
}

// readLogOverflowPolicy 解析日志队列溢出策略；缺省值保持同步写入兼容行为。
func readLogOverflowPolicy(config map[string]interface{}) (log.OverflowPolicy, error) {
	raw, exists := config["overflow_policy"]
	if !exists || raw == nil {
		return log.OverflowSync, nil
	}
	value, ok := raw.(string)
	if !ok {
		return log.OverflowSync, fmt.Errorf("%w: 日志配置 overflow_policy 的类型无效: %T", log.ErrInvalidOverflowPolicy, raw)
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sync":
		return log.OverflowSync, nil
	case "drop":
		return log.OverflowDrop, nil
	default:
		return log.OverflowSync, fmt.Errorf("%w: 日志配置 overflow_policy 的值无效: %q", log.ErrInvalidOverflowPolicy, value)
	}
}

func readLogFileOptions(config map[string]interface{}) (logDriver.FileOptions, error) {
	maxFileSize, err := readLogInteger(config, "max_file_size")
	if err != nil {
		return logDriver.FileOptions{}, err
	}
	retentionDays, err := readLogInteger(config, "retention_days")
	if err != nil {
		return logDriver.FileOptions{}, err
	}
	maxFiles, err := readLogInteger(config, "max_files")
	if err != nil {
		return logDriver.FileOptions{}, err
	}
	maxTotalSize, err := readLogInteger(config, "max_total_size")
	if err != nil {
		return logDriver.FileOptions{}, err
	}
	if retentionDays > int64(maxIntValue()) {
		return logDriver.FileOptions{}, fmt.Errorf("日志配置 retention_days 超出平台整数范围: %d", retentionDays)
	}
	if maxFiles > int64(maxIntValue()) {
		return logDriver.FileOptions{}, fmt.Errorf("日志配置 max_files 超出平台整数范围: %d", maxFiles)
	}
	return logDriver.FileOptions{
		MaxFileSize:   maxFileSize,
		RetentionDays: int(retentionDays),
		MaxFiles:      int(maxFiles),
		MaxTotalSize:  maxTotalSize,
	}, nil
}

func readLogInteger(config map[string]interface{}, key string) (int64, error) {
	raw, exists := config[key]
	if !exists || raw == nil {
		return 0, nil
	}
	if number, ok := raw.(json.Number); ok {
		value, err := number.Int64()
		if err != nil {
			return 0, fmt.Errorf("日志配置 %s 必须为整数: %w", key, err)
		}
		return value, nil
	}

	value := reflect.ValueOf(raw)
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		unsigned := value.Uint()
		if unsigned > math.MaxInt64 {
			return 0, fmt.Errorf("日志配置 %s 超出 int64 范围", key)
		}
		return int64(unsigned), nil
	case reflect.Float32, reflect.Float64:
		floating := value.Float()
		if math.IsNaN(floating) || math.IsInf(floating, 0) || math.Trunc(floating) != floating || floating > math.MaxInt64 || floating < math.MinInt64 {
			return 0, fmt.Errorf("日志配置 %s 必须是 int64 范围内的整数", key)
		}
		return int64(floating), nil
	default:
		return 0, invalidLogConfigValueError(key, raw)
	}
}

func readLogLevels(raw interface{}) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var values []interface{}
	switch typed := raw.(type) {
	case []interface{}:
		values = typed
	case []string:
		values = make([]interface{}, len(typed))
		for index, value := range typed {
			values[index] = value
		}
	default:
		return nil, invalidLogConfigValueError("level", raw)
	}
	levels := make([]string, 0, len(values))
	for _, rawLevel := range values {
		level, ok := rawLevel.(string)
		if !ok || strings.TrimSpace(level) == "" {
			return nil, invalidLogConfigValueError("level", rawLevel)
		}
		levels = append(levels, strings.ToLower(strings.TrimSpace(level)))
	}
	return levels, nil
}

func invalidLogConfigValueError(key string, value interface{}) error {
	return fmt.Errorf("日志配置 %s 的值无效: %T", key, value)
}

func maxIntValue() int {
	return int(^uint(0) >> 1)
}
