package config

import (
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"
)

type configSnapshotVisit struct {
	typ      reflect.Type
	pointer  uintptr
	length   int
	capacity int
}

func copyTypedConfigValue(input interface{}, depth int) (interface{}, error) {
	if input == nil {
		return nil, nil
	}
	value := reflect.ValueOf(input)
	copied, err := copyConfigReflect(value, depth, make(map[configSnapshotVisit]reflect.Value))
	if err != nil {
		return nil, err
	}
	return copied.Interface(), nil
}

func copyConfigReflect(value reflect.Value, depth int, visited map[configSnapshotVisit]reflect.Value) (reflect.Value, error) {
	if depth > maxConfigJSONDepth {
		return reflect.Value{}, fmt.Errorf("%w: 配置值嵌套过深", ErrConfigUnsupportedValue)
	}
	typ := value.Type()
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value, nil
		}
		copied, err := copyConfigReflect(value.Elem(), depth+1, visited)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(typ).Elem()
		result.Set(copied)
		return result, nil
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return copyConfigReference(value, depth, visited)
	case reflect.Array:
		result := reflect.New(typ).Elem()
		return copyConfigElements(result, value, depth, visited)
	case reflect.Struct:
		return copyConfigStruct(value, depth, visited)
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("%w: %s", ErrConfigUnsupportedValue, typ)
	default:
		return value, nil
	}
}

func copyConfigReference(value reflect.Value, depth int, visited map[configSnapshotVisit]reflect.Value) (reflect.Value, error) {
	if value.IsNil() {
		return value, nil
	}
	typ := value.Type()
	visit := configSnapshotVisit{typ: typ, pointer: value.Pointer()}
	if value.Kind() == reflect.Slice {
		visit.length, visit.capacity = value.Len(), value.Cap()
	}
	if copied, exists := visited[visit]; exists {
		if !copied.IsValid() {
			return reflect.Value{}, fmt.Errorf("%w: %s 含有循环引用", ErrConfigUnsupportedValue, typ)
		}
		return copied, nil
	}
	// 尚未完成的节点只作为循环检测标记。完成后复用副本，保留已共享的引用。
	visited[visit] = reflect.Value{}
	var result reflect.Value
	switch value.Kind() {
	case reflect.Pointer:
		elem, err := copyConfigReflect(value.Elem(), depth+1, visited)
		if err != nil {
			return reflect.Value{}, err
		}
		result = reflect.New(typ.Elem()).Convert(typ)
		result.Elem().Set(elem)
	case reflect.Map:
		result = reflect.MakeMapWithSize(typ, value.Len())
		entries := value.MapRange()
		for entries.Next() {
			key := entries.Key()
			if !configValueImmutable(key, depth+1) {
				return reflect.Value{}, fmt.Errorf("%w: %s 的键包含可变引用", ErrConfigUnsupportedValue, typ)
			}
			item, err := copyConfigReflect(entries.Value(), depth+1, visited)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(key, item)
		}
	case reflect.Slice:
		var err error
		result, err = copyConfigElements(reflect.MakeSlice(typ, value.Len(), value.Len()), value, depth, visited)
		if err != nil {
			return reflect.Value{}, err
		}
	}
	visited[visit] = result
	return result, nil
}

func copyConfigElements(result, value reflect.Value, depth int, visited map[configSnapshotVisit]reflect.Value) (reflect.Value, error) {
	for index := range value.Len() {
		item, err := copyConfigReflect(value.Index(index), depth+1, visited)
		if err != nil {
			return reflect.Value{}, err
		}
		result.Index(index).Set(item)
	}
	return result, nil
}

func copyConfigStruct(value reflect.Value, depth int, visited map[configSnapshotVisit]reflect.Value) (reflect.Value, error) {
	typ := value.Type()
	if typ == reflect.TypeFor[time.Time]() {
		return value, nil
	}
	if configStructHasIdentity(typ) {
		return reflect.Value{}, fmt.Errorf("%w: %s 含有锁或资源状态", ErrConfigUnsupportedValue, typ)
	}
	result := reflect.New(typ).Elem()
	hasPrivateFields := false
	for index := range value.NumField() {
		field := value.Field(index)
		if !typ.Field(index).IsExported() {
			if !configValueImmutable(field, depth+1) {
				return reflect.Value{}, fmt.Errorf("%w: %s.%s 为私有可变字段", ErrConfigUnsupportedValue, typ, typ.Field(index).Name)
			}
			hasPrivateFields = true
			continue
		}
		copied, err := copyConfigReflect(field, depth+1, visited)
		if err != nil {
			return reflect.Value{}, err
		}
		result.Field(index).Set(copied)
	}
	if !hasPrivateFields {
		return result, nil
	}
	// 所有字段验证完成后保留安全的私有字段，再以已隔离的公开字段覆盖原引用。
	withPrivate := reflect.New(typ).Elem()
	withPrivate.Set(value)
	for index := range value.NumField() {
		if typ.Field(index).IsExported() {
			withPrivate.Field(index).Set(result.Field(index))
		}
	}
	return withPrivate, nil
}

func configValueImmutable(value reflect.Value, depth int) bool {
	if depth > maxConfigJSONDepth {
		return false
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Interface:
		return value.IsNil() || configValueImmutable(value.Elem(), depth+1)
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[time.Time]() {
			return true
		}
		if configStructHasIdentity(value.Type()) {
			return false
		}
		for index := range value.NumField() {
			if !configValueImmutable(value.Field(index), depth+1) {
				return false
			}
		}
		return true
	case reflect.Array:
		for index := range value.Len() {
			if !configValueImmutable(value.Index(index), depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func configStructHasIdentity(typ reflect.Type) bool {
	pointer := reflect.PointerTo(typ)
	// 原子类型的 noCopy 标记也实现 Locker 接口，锁和资源对象不能复制。
	return pointer.Implements(reflect.TypeFor[sync.Locker]()) || pointer.Implements(reflect.TypeFor[io.Closer]())
}
