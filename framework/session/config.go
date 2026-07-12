package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	defaultSessionExpireSeconds = 1440
	defaultSessionMaxDataBytes  = 64 << 10
	maxSessionExpireSeconds     = 400 * 24 * 3600
	maxSessionDataBytes         = (1 << 20) - 4096
	maxSessionNameBytes         = 128
	maxSessionKeyBytes          = 256
)

var (
	// ErrInvalidSessionConfig 表示 Session 配置字段、类型或范围非法。
	ErrInvalidSessionConfig = errors.New("Session 配置非法")
	// ErrInvalidSessionDependency 表示驱动或 Cookie 工厂缺失、类型化 nil 或不满足协议。
	ErrInvalidSessionDependency = errors.New("Session 依赖非法")
	// ErrInvalidSessionKey 表示业务键为空、过长或包含控制字符。
	ErrInvalidSessionKey = errors.New("Session 键非法")
	// ErrInvalidSessionValue 表示值不能稳定编码为 JSON。
	ErrInvalidSessionValue = errors.New("Session 值非法")
	// ErrSessionDataTooLarge 表示单值或完整 Session 超过配置上限。
	ErrSessionDataTooLarge = errors.New("Session 数据超过大小上限")
	// ErrCorruptSession 表示后端 Session 信封损坏、版本未知或含有多余数据。
	ErrCorruptSession = errors.New("Session 存储数据损坏")
	// ErrSessionRevoked 表示当前 ID 已被轮换或销毁，旧请求不得重建它。
	ErrSessionRevoked = errors.New("Session 已撤销")
	// ErrSessionDestroyed 表示请求级 Session 已销毁，不允许继续修改。
	ErrSessionDestroyed = errors.New("Session 已销毁")
	// ErrSessionCookie 表示 Session Cookie 读取、签名或写入失败。
	ErrSessionCookie = errors.New("Session Cookie 操作失败")
	// ErrSessionIDCollision 表示新随机 ID 极低概率命中已有记录。
	ErrSessionIDCollision = errors.New("Session ID 冲突")
)

// Config 是启动期严格解析后按值复制的 Session 配置。
type Config struct {
	Name         string
	DriverType   string
	StoragePath  string
	CookiePath   string
	Expire       int
	Domain       string
	Secure       bool
	HttpOnly     bool
	SameSite     string
	MaxDataBytes int
}

// DefaultConfig 返回安全且完整的 Session 默认配置。
func DefaultConfig() Config {
	return Config{
		Name:         DefaultSessionName,
		DriverType:   "file",
		StoragePath:  "./runtime/session",
		CookiePath:   "/",
		Expire:       defaultSessionExpireSeconds,
		HttpOnly:     true,
		SameSite:     "Lax",
		MaxDataBytes: defaultSessionMaxDataBytes,
	}
}

// ParseConfig 严格解析 Session 配置，拒绝旧 path 歧义字段、未知字段和数值截断。
func ParseConfig(raw map[string]interface{}) (Config, error) {
	allowed := map[string]bool{
		"name": true, "type": true, "storage_path": true, "cookie_path": true,
		"expire": true, "domain": true, "secure": true, "httponly": true,
		"samesite": true, "max_data_bytes": true,
	}
	for key := range raw {
		if !allowed[key] {
			return Config{}, invalidSessionConfig(key, "未知配置项")
		}
	}
	config := DefaultConfig()
	var err error
	if value, exists := raw["name"]; exists {
		config.Name, err = sessionConfigString(value, "name")
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["type"]; exists {
		config.DriverType, err = sessionConfigString(value, "type")
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["storage_path"]; exists {
		config.StoragePath, err = sessionConfigString(value, "storage_path")
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["cookie_path"]; exists {
		config.CookiePath, err = sessionConfigString(value, "cookie_path")
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["expire"]; exists {
		config.Expire, err = sessionConfigInteger(value, "expire", 0, maxSessionExpireSeconds)
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["domain"]; exists {
		config.Domain, err = sessionConfigString(value, "domain")
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["secure"]; exists {
		config.Secure, err = sessionConfigBool(value, "secure")
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["httponly"]; exists {
		config.HttpOnly, err = sessionConfigBool(value, "httponly")
		if err != nil {
			return Config{}, err
		}
	}
	if value, exists := raw["samesite"]; exists {
		var rawSameSite string
		rawSameSite, err = sessionConfigString(value, "samesite")
		if err != nil {
			return Config{}, err
		}
		config.SameSite, err = normalizeSessionSameSite(rawSameSite)
		if err != nil {
			return Config{}, invalidSessionConfig("samesite", err.Error())
		}
	}
	if value, exists := raw["max_data_bytes"]; exists {
		config.MaxDataBytes, err = sessionConfigInteger(value, "max_data_bytes", 256, maxSessionDataBytes)
		if err != nil {
			return Config{}, err
		}
	}
	if err = validateSessionConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func validateSessionConfig(config Config) error {
	if config.Name == "" || len(config.Name) > maxSessionNameBytes || !utf8.ValidString(config.Name) ||
		hasSessionControl(config.Name) {
		return invalidSessionConfig("name", "为空、过长或包含控制字符")
	}
	if config.DriverType != "file" && config.DriverType != "memory" {
		return invalidSessionConfig("type", "仅支持 file 或 memory")
	}
	if strings.TrimSpace(config.StoragePath) == "" || hasSessionControl(config.StoragePath) {
		return invalidSessionConfig("storage_path", "不能为空或包含控制字符")
	}
	if config.Expire < 0 || config.Expire > maxSessionExpireSeconds {
		return invalidSessionConfig("expire", "超出允许范围")
	}
	if config.MaxDataBytes < 256 || config.MaxDataBytes > maxSessionDataBytes {
		return invalidSessionConfig("max_data_bytes", "超出允许范围")
	}
	if !config.HttpOnly {
		return invalidSessionConfig("httponly", "Session Cookie 必须启用 HttpOnly")
	}
	normalized, err := normalizeSessionSameSite(config.SameSite)
	if err != nil || normalized != config.SameSite {
		return invalidSessionConfig("samesite", "必须是规范形式 Lax、Strict 或 None")
	}
	probe := &http.Cookie{
		Name: config.Name, Value: "x", Path: config.CookiePath, Domain: config.Domain,
	}
	if err = probe.Valid(); err != nil || config.CookiePath == "" || !strings.HasPrefix(config.CookiePath, "/") ||
		strings.Contains(config.CookiePath, ";") || hasSessionControl(config.CookiePath) {
		return invalidSessionConfig("cookie_path/domain", fmt.Sprintf("Cookie 策略非法: %v", err))
	}
	return nil
}

func normalizeSessionSameSite(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "lax":
		return "Lax", nil
	case "strict":
		return "Strict", nil
	case "none":
		return "None", nil
	default:
		return "", errors.New("SameSite 仅支持 Lax、Strict、None")
	}
}

func sessionConfigString(value interface{}, field string) (string, error) {
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) || hasSessionControl(text) {
		return "", invalidSessionConfig(field, "必须是不含控制字符的字符串")
	}
	return text, nil
}

func sessionConfigBool(value interface{}, field string) (bool, error) {
	parsed, ok := value.(bool)
	if !ok {
		return false, invalidSessionConfig(field, "必须是布尔值")
	}
	return parsed, nil
}

func sessionConfigInteger(value interface{}, field string, minimum, maximum int) (int, error) {
	parsed, ok := exactSessionInteger(value)
	if !ok || parsed < int64(minimum) || parsed > int64(maximum) {
		return 0, invalidSessionConfig(field, fmt.Sprintf("必须是 %d 至 %d 的整数", minimum, maximum))
	}
	return int(parsed), nil
}

func exactSessionInteger(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case float32:
		return exactSessionFloat(float64(typed))
	case float64:
		return exactSessionFloat(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func exactSessionFloat(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < float64(math.MinInt64) || value >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(value), true
}

func invalidSessionConfig(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidSessionConfig, field, reason)
}

func hasSessionControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
