package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"thinkgo/framework"
	"thinkgo/framework/context"
)

var ErrInvalidHTTPConfig = errors.New("HTTP 配置非法")

type serverConf struct {
	Host              string
	Port              int
	EnableTLS         bool
	CertFile          string
	KeyFile           string
	EnableHTTP3       bool
	AllowedHosts      []string
	TrustedProxies    []string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	MultipartMemory   int64
}

type compressionConf struct {
	Enable  bool
	MinSize int
	Levels  map[string]int
}

func parseHTTPConfig(app *framework.App) (serverConf, compressionConf, error) {
	if app == nil || app.Config == nil || app.Route == nil || app.Middleware == nil {
		return serverConf{}, compressionConf{}, fmt.Errorf("%w: 应用、配置、路由和中间件必须完成初始化", ErrInvalidHTTPConfig)
	}
	serverValues, err := strictConfigMap(app.Config.Get("app.server", map[string]interface{}{}), "app.server")
	if err != nil {
		return serverConf{}, compressionConf{}, err
	}
	if err = rejectUnknownConfigKeys(serverValues, "app.server", map[string]bool{
		"host": true, "port": true, "allowed_hosts": true, "trusted_proxies": true,
		"tls": true, "http3": true, "read_header_timeout_ms": true, "read_timeout_ms": true,
		"write_timeout_ms": true, "idle_timeout_ms": true, "shutdown_timeout_ms": true,
		"max_header_bytes": true, "max_body_bytes": true, "multipart_max_memory_mb": true,
	}); err != nil {
		return serverConf{}, compressionConf{}, err
	}
	server, err := parseServerConfig(serverValues, app)
	if err != nil {
		return serverConf{}, compressionConf{}, err
	}
	compressionValues, err := strictConfigMap(app.Config.Get("app.compression", map[string]interface{}{}), "app.compression")
	if err != nil {
		return serverConf{}, compressionConf{}, err
	}
	if err = rejectUnknownConfigKeys(compressionValues, "app.compression", map[string]bool{
		"enable": true, "min_size": true, "levels": true, "level": true,
	}); err != nil {
		return serverConf{}, compressionConf{}, err
	}
	compression, err := parseCompressionConfig(compressionValues)
	if err != nil {
		return serverConf{}, compressionConf{}, err
	}
	return server, compression, nil
}

func parseServerConfig(values map[string]interface{}, app *framework.App) (serverConf, error) {
	config := serverConf{
		Host:              "0.0.0.0",
		Port:              8080,
		CertFile:          "./runtime/cert.pem",
		KeyFile:           "./runtime/key.pem",
		AllowedHosts:      []string{},
		TrustedProxies:    []string{},
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   5 * time.Second,
		MaxHeaderBytes:    1 << 20,
		MaxBodyBytes:      10 << 20,
		MultipartMemory:   32 << 20,
	}

	var err error
	if config.Host, err = configString(values, "host", config.Host); err != nil {
		return serverConf{}, err
	}
	if !isValidBindHost(config.Host) {
		return serverConf{}, invalidHTTPConfig("app.server.host %q 非法", config.Host)
	}
	port, err := configInteger(values, "port", int64(config.Port), 1, 65535)
	if err != nil {
		return serverConf{}, err
	}
	config.Port = int(port)

	config.AllowedHosts, err = configStringList(values, "allowed_hosts", config.AllowedHosts)
	if err != nil {
		return serverConf{}, err
	}
	config.TrustedProxies, err = configStringList(values, "trusted_proxies", config.TrustedProxies)
	if err != nil {
		return serverConf{}, err
	}

	tlsValues := map[string]interface{}{}
	if rawTLS, exists := values["tls"]; exists {
		tlsValues, err = strictConfigMap(rawTLS, "app.server.tls")
		if err != nil {
			return serverConf{}, err
		}
		if err = rejectUnknownConfigKeys(tlsValues, "app.server.tls", map[string]bool{
			"enable": true, "cert_file": true, "key_file": true,
		}); err != nil {
			return serverConf{}, err
		}
	}
	if config.EnableTLS, err = configBool(tlsValues, "enable", false); err != nil {
		return serverConf{}, err
	}
	if config.CertFile, err = configString(tlsValues, "cert_file", config.CertFile); err != nil {
		return serverConf{}, err
	}
	if config.KeyFile, err = configString(tlsValues, "key_file", config.KeyFile); err != nil {
		return serverConf{}, err
	}
	if config.EnableHTTP3, err = configBool(values, "http3", false); err != nil {
		return serverConf{}, err
	}
	if config.EnableHTTP3 && !config.EnableTLS {
		return serverConf{}, invalidHTTPConfig("启用 HTTP/3 时必须同时启用 TLS")
	}
	if config.EnableTLS && (strings.TrimSpace(config.CertFile) == "" || strings.TrimSpace(config.KeyFile) == "") {
		return serverConf{}, invalidHTTPConfig("启用 TLS 时证书和私钥路径不能为空")
	}

	if config.ReadHeaderTimeout, err = configDuration(values, "read_header_timeout_ms", config.ReadHeaderTimeout); err != nil {
		return serverConf{}, err
	}
	if config.ReadTimeout, err = configDuration(values, "read_timeout_ms", config.ReadTimeout); err != nil {
		return serverConf{}, err
	}
	if config.WriteTimeout, err = configDuration(values, "write_timeout_ms", config.WriteTimeout); err != nil {
		return serverConf{}, err
	}
	if config.IdleTimeout, err = configDuration(values, "idle_timeout_ms", config.IdleTimeout); err != nil {
		return serverConf{}, err
	}
	if config.ShutdownTimeout, err = configDuration(values, "shutdown_timeout_ms", config.ShutdownTimeout); err != nil {
		return serverConf{}, err
	}
	if config.ReadTimeout < config.ReadHeaderTimeout {
		return serverConf{}, invalidHTTPConfig("read_timeout_ms 不能小于 read_header_timeout_ms")
	}
	maxHeader, err := configInteger(values, "max_header_bytes", int64(config.MaxHeaderBytes), 1024, 64<<20)
	if err != nil {
		return serverConf{}, err
	}
	config.MaxHeaderBytes = int(maxHeader)
	if config.MaxBodyBytes, err = configInteger(values, "max_body_bytes", config.MaxBodyBytes, 1, 1<<30); err != nil {
		return serverConf{}, err
	}
	multipartMB, err := configInteger(values, "multipart_max_memory_mb", config.MultipartMemory>>20, 1, 1024)
	if err != nil {
		return serverConf{}, err
	}
	config.MultipartMemory = multipartMB << 20

	if app.Env != nil {
		if raw := strings.TrimSpace(app.Env.Get("SERVER_ALLOWED_HOSTS", "")); raw != "" {
			config.AllowedHosts, err = splitStrictConfigList(raw, "SERVER_ALLOWED_HOSTS")
			if err != nil {
				return serverConf{}, err
			}
		}
		if raw := strings.TrimSpace(app.Env.Get("SERVER_TRUSTED_PROXIES", "")); raw != "" {
			config.TrustedProxies, err = splitStrictConfigList(raw, "SERVER_TRUSTED_PROXIES")
			if err != nil {
				return serverConf{}, err
			}
		}
	}
	config.AllowedHosts, err = normalizeAllowedHosts(config.AllowedHosts)
	if err != nil {
		return serverConf{}, err
	}
	if _, err = context.NewRequest(nil, context.WithTrustedProxies(config.TrustedProxies)); err != nil {
		return serverConf{}, invalidHTTPConfig("受信代理配置错误: %v", err)
	}
	return config, nil
}

func parseCompressionConfig(values map[string]interface{}) (compressionConf, error) {
	config := compressionConf{MinSize: 1024, Levels: make(map[string]int)}
	var err error
	if config.Enable, err = configBool(values, "enable", false); err != nil {
		return compressionConf{}, err
	}
	minSize, err := configInteger(values, "min_size", int64(config.MinSize), 0, 8<<20)
	if err != nil {
		return compressionConf{}, err
	}
	config.MinSize = int(minSize)

	_, hasLevels := values["levels"]
	_, hasLegacyLevel := values["level"]
	if hasLevels && hasLegacyLevel {
		return compressionConf{}, invalidHTTPConfig("app.compression.level 与 levels 不能同时配置")
	}
	if hasLevels {
		levels, err := compressionLevelMap(values["levels"])
		if err != nil {
			return compressionConf{}, err
		}
		for algorithm, rawLevel := range levels {
			algorithm = strings.ToLower(strings.TrimSpace(algorithm))
			if _, duplicated := config.Levels[algorithm]; duplicated {
				return compressionConf{}, invalidHTTPConfig("压缩算法 %s 重复配置", algorithm)
			}
			level, err := exactConfigInteger(rawLevel)
			if err != nil || !validCompressionLevel(algorithm, level) {
				return compressionConf{}, invalidHTTPConfig("压缩算法 %s 的等级 %v 非法", algorithm, rawLevel)
			}
			config.Levels[algorithm] = int(level)
		}
	}
	if hasLegacyLevel {
		level, err := exactConfigInteger(values["level"])
		if err != nil || !validCompressionLevel("gzip", level) || !validCompressionLevel("br", level) {
			return compressionConf{}, invalidHTTPConfig("app.compression.level %v 非法", values["level"])
		}
		config.Levels["gzip"] = int(level)
		config.Levels["deflate"] = int(level)
		config.Levels["br"] = int(level)
		config.Levels["zstd"] = 2
	}
	return config, nil
}

func strictConfigMap(value interface{}, path string) (map[string]interface{}, error) {
	if value == nil {
		return map[string]interface{}{}, nil
	}
	result, ok := value.(map[string]interface{})
	if !ok {
		return nil, invalidHTTPConfig("%s 必须是对象，实际类型为 %T", path, value)
	}
	return result, nil
}

func rejectUnknownConfigKeys(values map[string]interface{}, path string, allowed map[string]bool) error {
	for key := range values {
		if !allowed[key] {
			return invalidHTTPConfig("%s 包含未知配置键 %q", path, key)
		}
	}
	return nil
}

func configString(values map[string]interface{}, key, defaultValue string) (string, error) {
	value, exists := values[key]
	if !exists {
		return defaultValue, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", invalidHTTPConfig("配置 %s 必须是字符串，实际类型为 %T", key, value)
	}
	return strings.TrimSpace(text), nil
}

func configBool(values map[string]interface{}, key string, defaultValue bool) (bool, error) {
	value, exists := values[key]
	if !exists {
		return defaultValue, nil
	}
	parsed, ok := value.(bool)
	if !ok {
		return false, invalidHTTPConfig("配置 %s 必须是布尔值，实际类型为 %T", key, value)
	}
	return parsed, nil
}

func configInteger(values map[string]interface{}, key string, defaultValue, minimum, maximum int64) (int64, error) {
	value, exists := values[key]
	if !exists {
		return defaultValue, nil
	}
	parsed, err := exactConfigInteger(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, invalidHTTPConfig("配置 %s 必须是 [%d,%d] 内的整数，实际为 %v", key, minimum, maximum, value)
	}
	return parsed, nil
}

func exactConfigInteger(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		if uint64(typed) <= math.MaxInt64 {
			return int64(typed), nil
		}
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed <= math.MaxInt64 {
			return int64(typed), nil
		}
	case float32:
		return exactConfigFloatInteger(float64(typed))
	case float64:
		return exactConfigFloatInteger(typed)
	case json.Number:
		return strconv.ParseInt(typed.String(), 10, 64)
	case string:
		return strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
	}
	return 0, errors.New("不是可表示的整数")
}

func exactConfigFloatInteger(value float64) (int64, error) {
	const maxInt64Exclusive = float64(uint64(1) << 63)
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < float64(math.MinInt64) || value >= maxInt64Exclusive {
		return 0, errors.New("浮点数不能无损转换为整数")
	}
	return int64(value), nil
}

func configDuration(values map[string]interface{}, key string, defaultValue time.Duration) (time.Duration, error) {
	milliseconds, err := configInteger(values, key, defaultValue.Milliseconds(), 1, (10 * time.Minute).Milliseconds())
	if err != nil {
		return 0, err
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func configStringList(values map[string]interface{}, key string, defaultValue []string) ([]string, error) {
	value, exists := values[key]
	if !exists {
		return append([]string(nil), defaultValue...), nil
	}
	switch typed := value.(type) {
	case []string:
		result := make([]string, len(typed))
		copy(result, typed)
		return validateNonEmptyStrings(result, key)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, invalidHTTPConfig("配置 %s 第 %d 项必须是字符串", key, index+1)
			}
			result = append(result, text)
		}
		return validateNonEmptyStrings(result, key)
	case string:
		return splitStrictConfigList(typed, key)
	default:
		return nil, invalidHTTPConfig("配置 %s 必须是字符串列表", key)
	}
}

func validateNonEmptyStrings(values []string, key string) ([]string, error) {
	result := make([]string, 0, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, invalidHTTPConfig("配置 %s 第 %d 项不能为空", key, index+1)
		}
		result = append(result, value)
	}
	return result, nil
}

func splitStrictConfigList(value, key string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return []string{}, nil
	}
	return validateNonEmptyStrings(strings.Split(value, ","), key)
}

func compressionLevelMap(value interface{}) (map[string]interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed, nil
	case map[string]int:
		result := make(map[string]interface{}, len(typed))
		for key, level := range typed {
			result[key] = level
		}
		return result, nil
	default:
		return nil, invalidHTTPConfig("app.compression.levels 必须是对象")
	}
}

func validCompressionLevel(algorithm string, level int64) bool {
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "gzip", "deflate":
		return level >= -2 && level <= 9
	case "br":
		return level >= 0 && level <= 11
	case "zstd":
		return level >= 1 && level <= 4
	default:
		return false
	}
}

func isValidBindHost(host string) bool {
	if host == "" {
		return false
	}
	trimmed := strings.Trim(host, "[]")
	return net.ParseIP(trimmed) != nil || isValidHostname(trimmed)
}

func normalizeAllowedHosts(hosts []string) ([]string, error) {
	result := make([]string, 0, len(hosts))
	seen := make(map[string]bool)
	for _, rawHost := range hosts {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rawHost), "."))
		if host == "*" {
			if !seen[host] {
				result = append(result, host)
				seen[host] = true
			}
			continue
		}
		wildcard := strings.HasPrefix(host, "*.")
		candidate := strings.TrimPrefix(host, "*.")
		normalized := normalizeHTTPHost(candidate)
		if normalized == "" {
			return nil, invalidHTTPConfig("allowed_hosts 项 %q 非法", rawHost)
		}
		if wildcard {
			host = "*." + normalized
		} else {
			host = normalized
		}
		if !seen[host] {
			result = append(result, host)
			seen[host] = true
		}
	}
	return result, nil
}

func isValidHostname(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, ":/\\@?#\x00\r\n\t ") {
		return false
	}
	for _, label := range strings.Split(strings.ToLower(host), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

// isAllowedHost 在配置白名单时校验 Host，防止 Host 伪造影响域名路由和绝对 URL。
func (h *Http) isAllowedHost(rawHost string) bool {
	host := normalizeHTTPHost(rawHost)
	if host == "" {
		return false
	}
	if len(h.srvConf.AllowedHosts) == 0 {
		return true
	}
	for _, allowed := range h.srvConf.AllowedHosts {
		switch {
		case allowed == "*":
			return true
		case strings.HasPrefix(allowed, "*."):
			base := strings.TrimPrefix(allowed, "*.")
			if host != base && strings.HasSuffix(host, "."+base) {
				return true
			}
		case host == allowed:
			return true
		}
	}
	return false
}

func normalizeHTTPHost(rawHost string) string {
	value := strings.TrimSpace(rawHost)
	if value == "" || strings.ContainsAny(value, "\x00\r\n\t ") {
		return ""
	}

	host := value
	if strings.HasPrefix(value, "[") {
		closingBracket := strings.IndexByte(value, ']')
		if closingBracket <= 1 {
			return ""
		}
		host = value[1:closingBracket]
		remainder := value[closingBracket+1:]
		if remainder != "" {
			if !strings.HasPrefix(remainder, ":") || !isValidHTTPPort(remainder[1:]) {
				return ""
			}
		}
		// 方括号只允许包裹 IPv6 字面量，拒绝多余括号和括号化 IPv4。
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() != nil {
			return ""
		}
	} else {
		if strings.ContainsAny(value, "[]") || strings.Count(value, ":") > 1 {
			return ""
		}
		if parsedHost, port, hasPort := strings.Cut(value, ":"); hasPort {
			if !isValidHTTPPort(port) {
				return ""
			}
			host = parsedHost
		}
	}

	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return ""
	}
	if net.ParseIP(host) == nil && !isValidHostname(host) {
		return ""
	}
	return strings.ToLower(host)
}

// isValidHTTPPort 严格限制 Host 端口为可监听的十进制端口，拒绝服务名和溢出值。
func isValidHTTPPort(port string) bool {
	if port == "" {
		return false
	}
	for _, char := range port {
		if char < '0' || char > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	return err == nil && parsed > 0
}

func invalidHTTPConfig(format string, values ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrInvalidHTTPConfig, fmt.Sprintf(format, values...))
}
