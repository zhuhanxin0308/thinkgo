package contract

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PreciseIntegerEncodingPrefix 标识需要按精确整数解码的缓存载荷编码前缀。
const PreciseIntegerEncodingPrefix = "integer:"

// ValidateCacheNumber 在写入前拒绝非法数字文本，所有后端保留相同失败语义。
func ValidateCacheNumber(value interface{}) error {
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	if number.String() == "" {
		return fmt.Errorf("%w: JSON 数字不能为空", ErrInvalidCacheValue)
	}
	if _, err := json.Marshal(number); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCacheValue, err)
	}
	return nil
}

// ValidCacheJSONInteger 只验证 JSON 整数语法，不把数值限制到机器整数范围。
func ValidCacheJSONInteger(text string) bool {
	return !strings.ContainsAny(text, ".eE") && ValidateCacheNumber(json.Number(text)) == nil
}

// EncodePreciseCacheInteger 避免 int64、uint64 和整数 json.Number 经 JSON 后端转成 float64 丢失精度。
func EncodePreciseCacheInteger(value interface{}) (interface{}, string) {
	switch typed := value.(type) {
	case int:
		if int64(typed) > 1<<53 || int64(typed) < -(1<<53) {
			return strconv.FormatInt(int64(typed), 10), PreciseIntegerEncodingPrefix + "int"
		}
	case int64:
		return strconv.FormatInt(typed, 10), PreciseIntegerEncodingPrefix + "int64"
	case uint:
		if uint64(typed) > 1<<53 {
			return strconv.FormatUint(uint64(typed), 10), PreciseIntegerEncodingPrefix + "uint"
		}
	case uint64:
		return strconv.FormatUint(typed, 10), PreciseIntegerEncodingPrefix + "uint64"
	case json.Number:
		if _, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			return typed.String(), PreciseIntegerEncodingPrefix + "json-number"
		}
		if ValidCacheJSONInteger(typed.String()) {
			return typed.String(), PreciseIntegerEncodingPrefix + "json-number-wide"
		}
	}
	return value, ""
}

// DecodePreciseCacheInteger 从精确整数编码还原原始 Go 整数或 json.Number 类型。
func DecodePreciseCacheInteger(value interface{}, encoding string) (interface{}, error) {
	if encoding == "" {
		return value, nil
	}
	text, ok := value.(string)
	if !ok || !strings.HasPrefix(encoding, PreciseIntegerEncodingPrefix) {
		return nil, ErrCorruptCacheEnvelope
	}
	kind := strings.TrimPrefix(encoding, PreciseIntegerEncodingPrefix)
	switch kind {
	case "json-number-wide":
		if !ValidCacheJSONInteger(text) {
			return nil, ErrCorruptCacheEnvelope
		}
		return json.Number(text), nil
	case "int", "int64", "json-number":
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, ErrCorruptCacheEnvelope
		}
		switch kind {
		case "int":
			if parsed < math.MinInt || parsed > math.MaxInt {
				return nil, ErrCorruptCacheEnvelope
			}
			return int(parsed), nil
		case "json-number":
			return json.Number(text), nil
		default:
			return parsed, nil
		}
	case "uint", "uint64":
		parsed, err := strconv.ParseUint(text, 10, 64)
		if err != nil || kind == "uint" && parsed > math.MaxUint {
			return nil, ErrCorruptCacheEnvelope
		}
		if kind == "uint" {
			return uint(parsed), nil
		}
		return parsed, nil
	default:
		return nil, ErrCorruptCacheEnvelope
	}
}
