package otlp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEndpoint           = "https://localhost:4318/v1/traces"
	defaultExporterTimeout    = 10 * time.Second
	defaultBatchTimeout       = 5 * time.Second
	defaultMaxExportBatchSize = 512
	defaultMaxQueueSize       = 2048
	maximumTelemetryTimeout   = 5 * time.Minute
	maximumExportBatchSize    = 4096
	maximumTelemetryQueueSize = 65536
	maximumHeaderJSONBytes    = 64 * 1024
	maximumHeaderCount        = 64
	maximumHeaderValueBytes   = 8 * 1024
)

var headerNamePattern = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)

type providerConfig struct {
	Enabled            bool
	ServiceName        string
	ServiceVersion     string
	Environment        string
	Endpoint           string
	Headers            map[string]string
	Timeout            time.Duration
	SampleRatio        float64
	BatchTimeout       time.Duration
	MaxExportBatchSize int
	MaxQueueSize       int
}

func parseProviderConfig(values map[string]interface{}, defaultServiceName, defaultVersion, defaultEnvironment string) (providerConfig, error) {
	allowed := map[string]struct{}{
		"enable": {}, "service_name": {}, "service_version": {}, "environment": {}, "endpoint": {},
		"headers": {}, "timeout_ms": {}, "sample_ratio": {}, "batch_timeout_ms": {},
		"max_export_batch_size": {}, "max_queue_size": {},
	}
	for key := range values {
		if _, exists := allowed[key]; !exists {
			return providerConfig{}, fmt.Errorf("telemetry 配置包含未知字段 %q", key)
		}
	}
	config := providerConfig{
		ServiceName:        strings.TrimSpace(defaultServiceName),
		ServiceVersion:     strings.TrimSpace(defaultVersion),
		Environment:        strings.TrimSpace(defaultEnvironment),
		Endpoint:           defaultEndpoint,
		Headers:            make(map[string]string),
		Timeout:            defaultExporterTimeout,
		SampleRatio:        1,
		BatchTimeout:       defaultBatchTimeout,
		MaxExportBatchSize: defaultMaxExportBatchSize,
		MaxQueueSize:       defaultMaxQueueSize,
	}
	var err error
	if config.Enabled, err = strictBool(values, "enable", false); err != nil {
		return providerConfig{}, err
	}
	if !config.Enabled {
		return config, nil
	}
	defaultName := config.ServiceName
	if config.ServiceName, err = strictString(values, "service_name", "", false); err != nil {
		return providerConfig{}, err
	}
	if config.ServiceName == "" {
		config.ServiceName = defaultName
	}
	defaultServiceVersion := config.ServiceVersion
	if config.ServiceVersion, err = strictString(values, "service_version", "", false); err != nil {
		return providerConfig{}, err
	}
	if config.ServiceVersion == "" {
		config.ServiceVersion = defaultServiceVersion
	}
	defaultDeploymentEnvironment := config.Environment
	if config.Environment, err = strictString(values, "environment", "", false); err != nil {
		return providerConfig{}, err
	}
	if config.Environment == "" {
		config.Environment = defaultDeploymentEnvironment
	}
	if config.ServiceName == "" || config.ServiceVersion == "" || config.Environment == "" {
		return providerConfig{}, errors.New("telemetry 服务名、版本和环境继承结果不能为空")
	}
	if config.Endpoint, err = strictString(values, "endpoint", config.Endpoint, true); err != nil {
		return providerConfig{}, err
	}
	if err = validateEndpoint(config.Endpoint); err != nil {
		return providerConfig{}, err
	}
	if config.Headers, err = strictHeaders(values["headers"]); err != nil {
		return providerConfig{}, err
	}
	timeoutMillis, err := strictInt(values, "timeout_ms", int(defaultExporterTimeout/time.Millisecond), 1, int(maximumTelemetryTimeout/time.Millisecond))
	if err != nil {
		return providerConfig{}, err
	}
	config.Timeout = time.Duration(timeoutMillis) * time.Millisecond
	batchMillis, err := strictInt(values, "batch_timeout_ms", int(defaultBatchTimeout/time.Millisecond), 1, int(maximumTelemetryTimeout/time.Millisecond))
	if err != nil {
		return providerConfig{}, err
	}
	config.BatchTimeout = time.Duration(batchMillis) * time.Millisecond
	if config.MaxExportBatchSize, err = strictInt(values, "max_export_batch_size", defaultMaxExportBatchSize, 1, maximumExportBatchSize); err != nil {
		return providerConfig{}, err
	}
	if config.MaxQueueSize, err = strictInt(values, "max_queue_size", defaultMaxQueueSize, 1, maximumTelemetryQueueSize); err != nil {
		return providerConfig{}, err
	}
	if config.MaxExportBatchSize > config.MaxQueueSize {
		return providerConfig{}, errors.New("telemetry.max_export_batch_size 不能超过 max_queue_size")
	}
	if config.SampleRatio, err = strictFloat(values, "sample_ratio", 1, 0, 1); err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func strictBool(values map[string]interface{}, key string, fallback bool) (bool, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("telemetry.%s 必须是布尔值", key)
	}
	return value, nil
}

func strictString(values map[string]interface{}, key, fallback string, required bool) (string, error) {
	raw, exists := values[key]
	if !exists {
		if required && strings.TrimSpace(fallback) == "" {
			return "", fmt.Errorf("telemetry.%s 不能为空", key)
		}
		return fallback, nil
	}
	value, ok := raw.(string)
	value = strings.TrimSpace(value)
	if !ok || required && value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("telemetry.%s 必须是安全非空字符串", key)
	}
	return value, nil
}

func strictHeaders(raw interface{}) (map[string]string, error) {
	if raw == nil {
		return make(map[string]string), nil
	}
	values, ok := raw.(map[string]interface{})
	if encoded, encodedOK := raw.(string); encodedOK {
		var err error
		values, err = decodeHeaderJSON(encoded)
		if err != nil {
			return nil, err
		}
	} else if !ok {
		return nil, errors.New("telemetry.headers 必须是字符串对象或 JSON 对象字符串")
	}
	if len(values) > maximumHeaderCount {
		return nil, fmt.Errorf("telemetry.headers 不能超过 %d 个", maximumHeaderCount)
	}
	result := make(map[string]string, len(values))
	for name, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok || !headerNamePattern.MatchString(name) || len(value) > maximumHeaderValueBytes || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("telemetry.headers 中的 %q 非法", name)
		}
		result[name] = value
	}
	return result, nil
}

func decodeHeaderJSON(encoded string) (map[string]interface{}, error) {
	if len(encoded) > maximumHeaderJSONBytes {
		return nil, fmt.Errorf("telemetry.headers JSON 不能超过 %d 字节", maximumHeaderJSONBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("telemetry.headers 必须是 JSON 对象字符串")
	}
	values := make(map[string]interface{})
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("解析 telemetry.headers 字段失败: %w", err)
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("telemetry.headers 字段名非法")
		}
		if _, duplicate := values[name]; duplicate {
			return nil, fmt.Errorf("telemetry.headers 包含重复字段 %q", name)
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("telemetry.headers.%s 必须是字符串", name)
		}
		values[name] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("telemetry.headers JSON 对象未正确结束")
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return nil, errors.New("telemetry.headers JSON 后存在多余内容")
	}
	return values, nil
}

func strictInt(values map[string]interface{}, key string, fallback, minimum, maximum int) (int, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	var value int64
	switch typed := raw.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("telemetry.%s 必须是整数", key)
		}
		value = parsed
	case int:
		value = int64(typed)
	case int64:
		value = typed
	case float64:
		if math.Trunc(typed) != typed {
			return 0, fmt.Errorf("telemetry.%s 必须是整数", key)
		}
		value = int64(typed)
	default:
		return 0, fmt.Errorf("telemetry.%s 必须是整数", key)
	}
	if value < int64(minimum) || value > int64(maximum) {
		return 0, fmt.Errorf("telemetry.%s 必须位于 %d 到 %d", key, minimum, maximum)
	}
	return int(value), nil
}

func strictFloat(values map[string]interface{}, key string, fallback, minimum, maximum float64) (float64, error) {
	raw, exists := values[key]
	if !exists {
		return fallback, nil
	}
	var value float64
	switch typed := raw.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil {
			return 0, fmt.Errorf("telemetry.%s 必须是数值", key)
		}
		value = parsed
	case float64:
		value = typed
	case int:
		value = float64(typed)
	default:
		return 0, fmt.Errorf("telemetry.%s 必须是数值", key)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < minimum || value > maximum {
		return 0, fmt.Errorf("telemetry.%s 必须位于 %.2f 到 %.2f", key, minimum, maximum)
	}
	return value, nil
}

func validateEndpoint(endpoint string) error {
	if strings.ContainsAny(endpoint, " \t\r\n\x00") {
		return errors.New("telemetry.endpoint 必须是无空白控制字符的绝对 HTTP(S) URL")
	}
	// 使用通用 URL 解析后逐字段拒绝凭据、查询和片段，保证地址只指向固定 Collector 端点。
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("telemetry.endpoint 必须是无凭据、查询和片段的绝对 HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("telemetry.endpoint 只支持 http 或 https")
	}
	return nil
}
