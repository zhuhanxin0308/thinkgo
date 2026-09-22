package context

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// ParamInt 将参数无损解析为当前平台的 int。
func (r *Request) ParamInt(key string, defaultValue int) int {
	value, ok := r.paramValue(key)
	if !ok {
		return defaultValue
	}
	parsed, valid := requestValueToInt64(value)
	if !valid || (strconv.IntSize == 32 && (parsed < math.MinInt32 || parsed > math.MaxInt32)) {
		return defaultValue
	}
	return int(parsed)
}

// ParamInt64 将参数无损解析为 int64。
func (r *Request) ParamInt64(key string, defaultValue int64) int64 {
	value, ok := r.paramValue(key)
	if !ok {
		return defaultValue
	}
	if parsed, valid := requestValueToInt64(value); valid {
		return parsed
	}
	return defaultValue
}

// ParamFloat 将参数解析为有限的 float64。
func (r *Request) ParamFloat(key string, defaultValue float64) float64 {
	value, ok := r.paramValue(key)
	if !ok {
		return defaultValue
	}
	if parsed, valid := requestValueToFloat64(value); valid {
		return parsed
	}
	return defaultValue
}

// ParamBool 只接受布尔值、0/1 和明确的常用布尔字符串。
func (r *Request) ParamBool(key string, defaultValue bool) bool {
	value, ok := r.paramValue(key)
	if !ok {
		return defaultValue
	}
	if typed, valid := value.(bool); valid {
		return typed
	}
	if numeric, valid := requestValueToFloat64(value); valid {
		switch numeric {
		case 0:
			return false
		case 1:
			return true
		default:
			return defaultValue
		}
	}
	text, valid := stringifyRequestValue(value)
	if !valid {
		return defaultValue
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "true", "on", "yes":
		return true
	case "false", "off", "no":
		return false
	default:
		return defaultValue
	}
}

func requestValueToInt64(value interface{}) (int64, bool) {
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
		if uint64(typed) <= math.MaxInt64 {
			return int64(typed), true
		}
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed <= math.MaxInt64 {
			return int64(typed), true
		}
	case float32:
		return exactFloatToInt64(float64(typed))
	case float64:
		return exactFloatToInt64(typed)
	case json.Number:
		return parseExactInt64(typed.String())
	case string:
		return parseExactInt64(strings.TrimSpace(typed))
	}
	return 0, false
}

func parseExactInt64(value string) (int64, bool) {
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsed, true
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return exactFloatToInt64(parsed)
}

func exactFloatToInt64(value float64) (int64, bool) {
	const maxInt64Exclusive = float64(uint64(1) << 63)
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, false
	}
	if value < float64(math.MinInt64) || value >= maxInt64Exclusive {
		return 0, false
	}
	return int64(value), true
}

func requestValueToFloat64(value interface{}) (float64, bool) {
	var parsed float64
	switch typed := value.(type) {
	case int:
		parsed = float64(typed)
	case int8:
		parsed = float64(typed)
	case int16:
		parsed = float64(typed)
	case int32:
		parsed = float64(typed)
	case int64:
		parsed = float64(typed)
	case uint:
		parsed = float64(typed)
	case uint8:
		parsed = float64(typed)
	case uint16:
		parsed = float64(typed)
	case uint32:
		parsed = float64(typed)
	case uint64:
		parsed = float64(typed)
	case float32:
		parsed = float64(typed)
	case float64:
		parsed = typed
	case json.Number:
		value, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		parsed = value
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		parsed = value
	default:
		return 0, false
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

// stringifyRequestValue 仅格式化可信标量，禁止隐式调用不受信对象的 String 方法。
func stringifyRequestValue(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", true
	case string:
		return typed, true
	case []string:
		return strings.Join(typed, ","), true
	case bool:
		return strconv.FormatBool(typed), true
	case int:
		return strconv.FormatInt(int64(typed), 10), true
	case int8:
		return strconv.FormatInt(int64(typed), 10), true
	case int16:
		return strconv.FormatInt(int64(typed), 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	case float32:
		value := float64(typed)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", false
		}
		return strconv.FormatFloat(value, 'g', -1, 32), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", false
		}
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	case json.Number:
		raw := typed.String()
		if raw == "" || !json.Valid([]byte(raw)) || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
			return "", false
		}
		return raw, true
	default:
		return "", false
	}
}

func normalizeStringSliceValue(values []string) interface{} {
	if len(values) == 1 {
		return values[0]
	}
	return append([]string(nil), values...)
}

func deepCloneRequestValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = deepCloneRequestValue(item)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			result[index] = deepCloneRequestValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func firstDefault(defaults []string) string {
	if len(defaults) > 0 {
		return defaults[0]
	}
	return ""
}
