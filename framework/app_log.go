package framework

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"

	"thinkgo/framework/log"
	logDriver "thinkgo/framework/log/driver"
)

// createAppLogChannel 严格解析单个日志通道，配置错误在启动阶段立即返回。
func createAppLogChannel(app *App, channelConfig map[string]interface{}, addConsole bool) (*log.Log, error) {
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

	levels, err := readLogLevels(channelConfig["level"])
	if err != nil {
		return nil, err
	}
	path := app.BasePath + RuntimeLogDir
	if value, exists := channelConfig["path"]; exists {
		typed, ok := value.(string)
		if !ok || strings.TrimSpace(typed) == "" {
			return nil, invalidLogConfigValueError("path", value)
		}
		path = strings.TrimSpace(typed)
		if !filepath.IsAbs(path) {
			path = filepath.Join(app.BasePath, path)
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
	if addConsole {
		if err := logger.AddDriver(logDriver.NewConsole()); err != nil {
			_ = logger.Close()
			return nil, fmt.Errorf("添加控制台日志驱动失败: %w", err)
		}
		logger.SetCallerEnabled(true)
	}
	logger.SetLevels(levels)
	return logger, nil
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
