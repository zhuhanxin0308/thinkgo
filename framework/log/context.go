package log

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
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

var sensitiveDriverErrorPattern = regexp.MustCompile(`(?i)(authorization|cookie|password|passwd|secret|session|token|api[-_]?key|refresh[-_]?token)(\s*[:=]\s*)([^\s,;&]+)`)

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
}

type logReference struct {
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

// cloneContext 在日志入队前生成完整快照，避免调用方后续修改类型化集合或结构体。
func cloneContext(ctx map[string]interface{}) map[string]interface{} {
	if len(ctx) == 0 {
		return nil
	}

	cloned := cloneLogReflectValue(reflect.ValueOf(ctx), make(map[logReference]reflect.Value))
	if !cloned.IsValid() || cloned.IsNil() {
		return nil
	}
	return cloned.Interface().(map[string]interface{})
}

func cloneLogReflectValue(value reflect.Value, visited map[logReference]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}

	switch value.Kind() {
	case reflect.Interface:
		cloned := reflect.New(value.Type()).Elem()
		if value.IsNil() {
			return cloned
		}
		inner := cloneLogReflectValue(value.Elem(), visited)
		if inner.IsValid() {
			cloned.Set(inner)
		}
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		reference := newLogReference(value)
		if cloned, exists := visited[reference]; exists {
			return cloned
		}
		cloned := reflect.New(value.Type().Elem())
		visited[reference] = cloned
		setClonedValue(cloned.Elem(), cloneLogReflectValue(value.Elem(), visited))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		reference := newLogReference(value)
		if cloned, exists := visited[reference]; exists {
			return cloned
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[reference] = cloned
		iterator := value.MapRange()
		for iterator.Next() {
			clonedKey := cloneLogReflectValue(iterator.Key(), visited)
			clonedValue := cloneLogReflectValue(iterator.Value(), visited)
			if clonedKey.IsValid() && clonedValue.IsValid() {
				cloned.SetMapIndex(clonedKey, clonedValue)
			}
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		reference := newLogReference(value)
		if cloned, exists := visited[reference]; exists {
			return cloned
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		visited[reference] = cloned
		for index := 0; index < value.Len(); index++ {
			setClonedValue(cloned.Index(index), cloneLogReflectValue(value.Index(index), visited))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			setClonedValue(cloned.Index(index), cloneLogReflectValue(value.Index(index), visited))
		}
		return cloned
	case reflect.Struct:
		cloned := reflect.New(value.Type()).Elem()
		// 先复制整个值，以保留 time.Time 等含不可导出字段的不可变值语义。
		cloned.Set(value)
		for index := 0; index < value.NumField(); index++ {
			target := cloned.Field(index)
			source := value.Field(index)
			if !target.CanSet() || !source.CanInterface() {
				continue
			}
			setClonedValue(target, cloneLogReflectValue(source, visited))
		}
		return cloned
	default:
		return value
	}
}

func setClonedValue(target reflect.Value, value reflect.Value) {
	if !target.CanSet() || !value.IsValid() {
		return
	}
	if value.Type().AssignableTo(target.Type()) {
		target.Set(value)
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
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		floatValue := value.Float()
		if math.IsNaN(floatValue) || math.IsInf(floatValue, 0) {
			return fmt.Sprint(floatValue)
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

// sanitizeDriverErrorText 防止驱动错误通过兜底通道注入换行或泄露常见凭据。
func sanitizeDriverErrorText(message string) string {
	singleLine := escapeLogLineValue(message)
	return sensitiveDriverErrorPattern.ReplaceAllString(singleLine, `${1}${2}`+logRedactedPlaceholder)
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
