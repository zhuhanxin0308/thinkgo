package log

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	logRedactedPlaceholder = "[REDACTED]"
	logCircularPlaceholder = "[CIRCULAR]"
	logUnsupportedValue    = "[UNSUPPORTED]"
)

var timeType = reflect.TypeOf(time.Time{})

var logLineEscaper = strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`)

var logKeyNormalizer = strings.NewReplacer("-", "_", ".", "_")

var sensitiveLogKeyParts = [...]string{
	"authorization",
	"cookie",
	"password",
	"passwd",
	"secret",
	"session",
	"token",
	"api_key",
	"apikey",
	"refresh_token",
	"private_key",
	"privatekey",
	"signing_key",
	"signingkey",
	"encryption_key",
	"encryptionkey",
	"access_key",
	"accesskey",
	"credential_value",
	"client_secret",
	"clientsecret",
}

type logReference struct {
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

// cloneContext 为驱动复制已脱敏的树，输入已经不包含指针、循环或任意用户类型。
func cloneContext(ctx map[string]interface{}) map[string]interface{} {
	if ctx == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(ctx))
	for key, value := range ctx {
		cloned[key] = cloneSanitizedLogValue(value)
	}
	return cloned
}

func cloneSanitizedLogValue(value interface{}) interface{} {
	switch value := value.(type) {
	case map[string]interface{}:
		return cloneContext(value)
	case []interface{}:
		cloned := make([]interface{}, len(value))
		for index, item := range value {
			cloned[index] = cloneSanitizedLogValue(item)
		}
		return cloned
	default:
		return value
	}
}

func newLogReference(value reflect.Value) logReference {
	reference := logReference{typeOf: value.Type(), pointer: value.Pointer()}
	if value.Kind() == reflect.Slice {
		reference.length = value.Len()
	}
	return reference
}

// sanitizeLogContext 将所有可序列化结构转换为安全副本，并按字段名递归脱敏。
func sanitizeLogContext(ctx map[string]interface{}) map[string]interface{} {
	sanitized, ok := sanitizeLogReflectValue(reflect.ValueOf(ctx), "", make(map[logReference]int)).(map[string]interface{})
	if !ok {
		return nil
	}
	return sanitized
}

func sanitizeLogReflectValue(value reflect.Value, key string, visiting map[logReference]int) interface{} {
	if isSensitiveLogKey(key) {
		return logRedactedPlaceholder
	}
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return sanitizeLogReflectValue(value.Elem(), key, visiting)
	}

	if isLogReferenceKind(value.Kind()) {
		if value.IsNil() {
			return nil
		}
		reference := newLogReference(value)
		if visiting[reference] > 0 {
			return logCircularPlaceholder
		}
		visiting[reference]++
		defer func() {
			visiting[reference]--
			if visiting[reference] == 0 {
				delete(visiting, reference)
			}
		}()
	}

	switch value.Kind() {
	case reflect.Pointer:
		return sanitizeLogReflectValue(value.Elem(), key, visiting)
	case reflect.Map:
		sanitized := make(map[string]interface{}, value.Len())
		iterator := value.MapRange()
		unsupportedKeyIndex := 0
		for iterator.Next() {
			innerKey := logMapKey(iterator.Key())
			if innerKey == logUnsupportedValue {
				innerKey = fmt.Sprintf("%s_%d", logUnsupportedValue, unsupportedKeyIndex)
				unsupportedKeyIndex++
			}
			sanitized[innerKey] = sanitizeLogReflectValue(iterator.Value(), innerKey, visiting)
		}
		return sanitized
	case reflect.Slice, reflect.Array:
		sanitized := make([]interface{}, value.Len())
		for index := 0; index < value.Len(); index++ {
			sanitized[index] = sanitizeLogReflectValue(value.Index(index), key, visiting)
		}
		return sanitized
	case reflect.Struct:
		if value.Type() == timeType && value.CanInterface() {
			return value.Interface()
		}
		return sanitizeLogStruct(value, visiting)
	case reflect.Bool:
		return value.Bool()
	case reflect.String:
		return value.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Type().PkgPath() == "" {
			return value.Interface()
		}
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if value.Type().PkgPath() == "" {
			return value.Interface()
		}
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		floatValue := value.Float()
		if math.IsNaN(floatValue) || math.IsInf(floatValue, 0) {
			return fmt.Sprint(floatValue)
		}
		if value.Type().PkgPath() == "" {
			return value.Interface()
		}
		return floatValue
	case reflect.Invalid:
		return nil
	default:
		return logUnsupportedValue
	}
}

func sanitizeLogStruct(value reflect.Value, visiting map[logReference]int) map[string]interface{} {
	typeOf := value.Type()
	sanitized := make(map[string]interface{})
	for index := 0; index < value.NumField(); index++ {
		field := typeOf.Field(index)
		if field.PkgPath != "" {
			continue
		}
		fieldName, included := logJSONFieldName(field)
		if !included {
			continue
		}
		sanitized[fieldName] = sanitizeLogReflectValue(value.Field(index), fieldName, visiting)
	}
	return sanitized
}

func logJSONFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		name = field.Name
	}
	return name, true
}

func logMapKey(value reflect.Value) string {
	switch value.Kind() {
	case reflect.String:
		return value.String()
	case reflect.Bool:
		return strconv.FormatBool(value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(value.Uint(), 10)
	default:
		return logUnsupportedValue
	}
}

func isLogReferenceKind(kind reflect.Kind) bool {
	return kind == reflect.Pointer || kind == reflect.Map || kind == reflect.Slice
}

func isSensitiveLogKey(key string) bool {
	normalized := strings.ToLower(logKeyNormalizer.Replace(key))
	for _, part := range sensitiveLogKeyParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

// SanitizeErrorText 脱敏错误文本中的常见凭据键值，供跨包错误日志复用。
// 长凭据只保留前四个字符，短凭据全部隐藏；结构化敏感字段仍使用完整遮蔽。
func SanitizeErrorText(message string) string {
	return sanitizeDriverErrorText(message)
}

// SanitizeErrorTextFully 供公开错误及调试响应使用，不暴露日志策略允许保留的凭据前缀。
func SanitizeErrorTextFully(message string) string {
	return escapeLogLineValue(redactErrorCredentialsWithMask(message, func(value string) string {
		if value == "" {
			return value
		}
		return logRedactedPlaceholder
	}))
}

// sanitizeDriverErrorText 防止驱动错误通过兜底通道注入换行或泄露常见凭据。
func sanitizeDriverErrorText(message string) string {
	return escapeLogLineValue(redactErrorCredentials(message))
}

func escapeLogLineValue(value string) string {
	return logLineEscaper.Replace(value)
}

// marshalSanitizedContext 始终返回合法 JSON，避免异常值让整条上下文静默丢失。
func marshalSanitizedContext(ctx map[string]interface{}) string {
	encoded, err := json.Marshal(sanitizeLogContext(ctx))
	if err != nil {
		return `{"log_context":"[UNSERIALIZABLE]"}`
	}
	return string(encoded)
}
