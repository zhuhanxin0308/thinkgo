package exception

import (
	"encoding/json"
	"strings"
)

// sanitizeJSONBody 对调试页中的 JSON 递归脱敏，解析失败时由调用方隐藏整个请求体。
func sanitizeJSONBody(body string) (string, bool) {
	var payload interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", false
	}

	payload = sanitizeJSONValue("", payload)
	sanitizedBody, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(sanitizedBody), true
}

func sanitizeJSONValue(key string, value interface{}) interface{} {
	if isSensitiveKey(key) {
		return redactedPlaceholder
	}

	switch typed := value.(type) {
	case map[string]interface{}:
		sanitized := make(map[string]interface{}, len(typed))
		for innerKey, innerValue := range typed {
			sanitized[innerKey] = sanitizeJSONValue(innerKey, innerValue)
		}
		return sanitized
	case []interface{}:
		sanitized := make([]interface{}, len(typed))
		for index, item := range typed {
			sanitized[index] = sanitizeJSONValue(key, item)
		}
		return sanitized
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	if lowerKey == "" {
		return false
	}

	sensitiveKeys := []string{
		"authorization",
		"cookie",
		"set-cookie",
		"password",
		"passwd",
		"token",
		"secret",
		"session",
		"api_key",
		"apikey",
		"access_key",
		"refresh_token",
	}

	for _, sensitiveKey := range sensitiveKeys {
		if lowerKey == sensitiveKey || strings.Contains(lowerKey, sensitiveKey) {
			return true
		}
	}
	return false
}
