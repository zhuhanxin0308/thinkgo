// Package cachesnapshot 为缓存驱动和共享加载提供一致的可变对象复制规则。
package cachesnapshot

import (
	"errors"
	"fmt"
	"reflect"
	"time"
)

// ErrUnsupportedValue 保留旧内存驱动的错误身份与消息。
var ErrUnsupportedValue = errors.New("内存缓存不支持安全复制该值类型")

type memoryCloneVisit struct {
	typ      reflect.Type
	kind     reflect.Kind
	pointer  uintptr
	length   int
	capacity int
}

// Clone 复制常见可变值，避免缓存内部状态被调用方通过 map、slice 或指针旁路修改。
func Clone(value interface{}) (cloned interface{}, cloneErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cloned = nil
			cloneErr = fmt.Errorf("%w: %v", ErrUnsupportedValue, recovered)
		}
	}()
	if value == nil {
		return nil, nil
	}
	switch value.(type) {
	case bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr,
		float32, float64, complex64, complex128, time.Time:
		return value, nil
	}
	result := cloneMemoryReflect(reflect.ValueOf(value), make(map[memoryCloneVisit]reflect.Value), &cloneErr)
	if cloneErr != nil {
		return nil, cloneErr
	}
	if !result.IsValid() {
		return nil, nil
	}
	return result.Interface(), nil
}

func cloneMemoryReflect(value reflect.Value, visited map[memoryCloneVisit]reflect.Value, cloneErr *error) reflect.Value {
	if !value.IsValid() || *cloneErr != nil {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneMemoryReflect(value.Elem(), visited, cloneErr)
		result := reflect.New(value.Type()).Elem()
		if cloned.IsValid() && cloned.Type().AssignableTo(value.Type()) {
			result.Set(cloned)
		}
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		// 命名指针可以与普通指针指向同一对象，用目标类型识别对象，再恢复字段的指针类型。
		visit := memoryCloneVisit{typ: value.Type().Elem(), kind: value.Kind(), pointer: value.Pointer()}
		if cloned, exists := visited[visit]; exists {
			return cloned.Convert(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		visited[visit] = result
		result.Elem().Set(cloneMemoryReflect(value.Elem(), visited, cloneErr))
		return result.Convert(value.Type())
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := memoryCloneVisit{typ: value.Type(), kind: value.Kind(), pointer: value.Pointer()}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[visit] = result
		iter := value.MapRange()
		for iter.Next() {
			if !memorySnapshotImmutableKey(iter.Key()) {
				*cloneErr = fmt.Errorf("%w: map 键包含可变引用", ErrUnsupportedValue)
				return reflect.Zero(value.Type())
			}
			result.SetMapIndex(iter.Key(), cloneMemoryReflect(iter.Value(), visited, cloneErr))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := memoryCloneVisit{
			typ:      value.Type(),
			kind:     value.Kind(),
			pointer:  value.Pointer(),
			length:   value.Len(),
			capacity: value.Cap(),
		}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		visited[visit] = result
		// 标量元素不包含可变引用（字符串底层数据不可变）；批量复制保留命名类型与隔离。
		// 含 map、slice、指针或结构体的元素仍走递归，不能把浅复制当作深快照。
		switch value.Type().Elem().Kind() {
		case reflect.Bool, reflect.String,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
			reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
			reflect.Copy(result, value)
			return result
		}
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneMemoryReflect(value.Index(index), visited, cloneErr))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneMemoryReflect(value.Index(index), visited, cloneErr))
		}
		return result
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[time.Time]() {
			return value
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if !result.Field(index).CanSet() {
				if !memorySnapshotImmutableType(value.Field(index).Type()) {
					*cloneErr = fmt.Errorf("%w: %s.%s 为私有可变字段", ErrUnsupportedValue, value.Type(), value.Type().Field(index).Name)
					return reflect.Zero(value.Type())
				}
				continue
			}
			result.Field(index).Set(cloneMemoryReflect(value.Field(index), visited, cloneErr))
		}
		return result
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		*cloneErr = fmt.Errorf("%w: %s", ErrUnsupportedValue, value.Type())
		return reflect.Zero(value.Type())
	default:
		return value
	}
}
