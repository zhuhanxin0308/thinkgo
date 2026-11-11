package driver

import (
	"reflect"
	"time"
)

// memorySnapshotImmutableType 只允许按值复制后不会共享可变存储的私有字段和 map 键。
// time.Time 的内部时区属于标准库不可变值语义，保持既有时间缓存兼容性。
func memorySnapshotImmutableType(value reflect.Type) bool {
	if value == reflect.TypeFor[time.Time]() {
		return true
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Array:
		return memorySnapshotImmutableType(value.Elem())
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if !memorySnapshotImmutableType(value.Field(index).Type) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// memorySnapshotImmutableKey 检查接口键的实际值，不能因静态接口类型拒绝安全的标量键。
func memorySnapshotImmutableKey(value reflect.Value) bool {
	if memorySnapshotImmutableType(value.Type()) {
		return true
	}
	switch value.Kind() {
	case reflect.Interface:
		return value.IsNil() || memorySnapshotImmutableKey(value.Elem())
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if !memorySnapshotImmutableKey(value.Field(index)) {
				return false
			}
		}
		return true
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if !memorySnapshotImmutableKey(value.Index(index)) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
