package context

import (
	"encoding/json"
	"reflect"
	"time"
)

var (
	jsonTimeType        = reflect.TypeOf(time.Time{})
	jsonTimePointerType = reflect.TypeOf((*time.Time)(nil))
	jsonMarshalerType   = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
)

type jsonTimeVisit struct {
	typ      reflect.Type
	pointer  uintptr
	length   int
	capacity int
}

// NormalizeJSONTimes 将标准 time.Time 递归转换为 UTC，统一 HTTP JSON 时间协议。
func NormalizeJSONTimes(value interface{}) interface{} {
	if !containsJSONTime(reflect.ValueOf(value), make(map[jsonTimeVisit]struct{})) {
		return value
	}
	normalized := normalizeJSONTimeValue(reflect.ValueOf(value), make(map[jsonTimeVisit]reflect.Value))
	if !normalized.IsValid() {
		return nil
	}
	return normalized.Interface()
}

// containsJSONTime 先用只读反射探测时间值，避免普通 JSON 结果被无条件深拷贝。
func containsJSONTime(value reflect.Value, visited map[jsonTimeVisit]struct{}) bool {
	if !value.IsValid() {
		return false
	}
	if value.Type() == jsonTimeType {
		return true
	}
	if value.Type() == jsonTimePointerType {
		return !value.IsNil()
	}
	if value.Type().Implements(jsonMarshalerType) {
		return false
	}

	switch value.Kind() {
	case reflect.Interface:
		return !value.IsNil() && containsJSONTime(value.Elem(), visited)
	case reflect.Pointer:
		if value.IsNil() {
			return false
		}
		visit := jsonTimeVisit{typ: value.Type(), pointer: value.Pointer()}
		if _, exists := visited[visit]; exists {
			return false
		}
		visited[visit] = struct{}{}
		return containsJSONTime(value.Elem(), visited)
	case reflect.Map:
		if value.IsNil() {
			return false
		}
		visit := jsonTimeVisit{typ: value.Type(), pointer: value.Pointer()}
		if _, exists := visited[visit]; exists {
			return false
		}
		visited[visit] = struct{}{}
		iter := value.MapRange()
		for iter.Next() {
			if containsJSONTime(iter.Value(), visited) {
				return true
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			return false
		}
		visit := jsonTimeVisit{typ: value.Type(), pointer: value.Pointer(), length: value.Len(), capacity: value.Cap()}
		if _, exists := visited[visit]; exists {
			return false
		}
		visited[visit] = struct{}{}
		for index := 0; index < value.Len(); index++ {
			if containsJSONTime(value.Index(index), visited) {
				return true
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if containsJSONTime(value.Index(index), visited) {
				return true
			}
		}
	case reflect.Struct:
		if !value.CanInterface() {
			return false
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath != "" || !value.Field(index).CanInterface() {
				continue
			}
			if containsJSONTime(value.Field(index), visited) {
				return true
			}
		}
	}
	return false
}

func normalizeJSONTimeValue(value reflect.Value, visited map[jsonTimeVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	if value.Type() == jsonTimeType {
		return reflect.ValueOf(value.Interface().(time.Time).UTC())
	}
	if value.Type() == jsonTimePointerType {
		if value.IsNil() {
			return value
		}
		utc := value.Interface().(*time.Time).UTC()
		return reflect.ValueOf(&utc)
	}
	if value.Type().Implements(jsonMarshalerType) {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		return normalizeJSONTimeValue(value.Elem(), visited)
	case reflect.Pointer:
		if value.IsNil() {
			return value
		}
		visit := jsonTimeVisit{typ: value.Type(), pointer: value.Pointer()}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		clone := reflect.New(value.Type().Elem())
		visited[visit] = clone
		setNormalizedValue(clone.Elem(), normalizeJSONTimeValue(value.Elem(), visited), value.Elem())
		return clone
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		visit := jsonTimeVisit{typ: value.Type(), pointer: value.Pointer()}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		clone := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[visit] = clone
		for _, key := range value.MapKeys() {
			original := value.MapIndex(key)
			normalized := normalizeJSONTimeValue(original, visited)
			if !normalized.IsValid() || !normalized.Type().AssignableTo(value.Type().Elem()) {
				normalized = original
			}
			clone.SetMapIndex(key, normalized)
		}
		return clone
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		visit := jsonTimeVisit{typ: value.Type(), pointer: value.Pointer(), length: value.Len(), capacity: value.Cap()}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		visited[visit] = clone
		for index := 0; index < value.Len(); index++ {
			setNormalizedValue(clone.Index(index), normalizeJSONTimeValue(value.Index(index), visited), value.Index(index))
		}
		return clone
	case reflect.Array:
		clone := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			setNormalizedValue(clone.Index(index), normalizeJSONTimeValue(value.Index(index), visited), value.Index(index))
		}
		return clone
	case reflect.Struct:
		if !value.CanInterface() {
			return value
		}
		clone := reflect.New(value.Type()).Elem()
		clone.Set(value)
		for index := 0; index < value.NumField(); index++ {
			fieldType := value.Type().Field(index)
			if fieldType.PkgPath != "" {
				continue
			}
			source := value.Field(index)
			if !source.CanInterface() || !clone.Field(index).CanSet() {
				continue
			}
			setNormalizedValue(clone.Field(index), normalizeJSONTimeValue(source, visited), source)
		}
		return clone
	default:
		return value
	}
}

// setNormalizedValue 将递归结果安全写回原始容器，保留无法转换的原值。
func setNormalizedValue(target, normalized, fallback reflect.Value) {
	if !target.CanSet() {
		return
	}
	if normalized.IsValid() && normalized.Type().AssignableTo(target.Type()) {
		target.Set(normalized)
		return
	}
	if fallback.IsValid() && fallback.Type().AssignableTo(target.Type()) {
		target.Set(fallback)
		return
	}
	target.Set(reflect.Zero(target.Type()))
}
