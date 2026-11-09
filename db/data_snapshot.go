package db

import (
	"fmt"
	"reflect"
	"time"
)

const (
	// 深度包含接口和指针层，给合法嵌套容器留出余量，同时限制调用栈。
	maxDatabaseValueDepth = 128
	// 按访问节点计费，配合共享子图缓存限制遍历和分配总量。
	maxDatabaseValueNodes = 1 << 20
)

type databaseValueVisit struct {
	typ     reflect.Type
	pointer uintptr
	length  int
}

type databaseValueSnapshot struct {
	active    map[databaseValueVisit]bool
	completed map[databaseValueVisit]reflect.Value
	remaining int
	location  *time.Location
}

// snapshotDatabaseValue 统一负责复制、环检测、预算以及写入时间转换，不调用用户的 Valuer 方法。
func snapshotDatabaseValue(value interface{}, location *time.Location) (interface{}, error) {
	state := databaseValueSnapshot{remaining: maxDatabaseValueNodes, location: location}
	return state.snapshot(value)
}

// snapshot 对不可变标量直接保留值，仅复杂对象启用反射与引用身份表。
func (state *databaseValueSnapshot) snapshot(value interface{}) (interface{}, error) {
	if state.remaining <= 0 {
		return nil, fmt.Errorf("%w: 参数快照超过节点预算", ErrInvalidQuery)
	}
	if value == nil || databaseScalarKind(reflect.TypeOf(value).Kind()) {
		state.remaining--
		return value, nil
	}
	cloned, err := state.copy(reflect.ValueOf(value), 0)
	if err != nil {
		return nil, err
	}
	return cloned.Interface(), nil
}

func databaseScalarKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.String:
		return true
	default:
		return false
	}
}

func (state *databaseValueSnapshot) copy(value reflect.Value, depth int) (copied reflect.Value, copyErr error) {
	if depth >= maxDatabaseValueDepth || state.remaining <= 0 {
		return reflect.Value{}, fmt.Errorf("%w: 参数快照超过深度或节点预算", ErrInvalidQuery)
	}
	state.remaining--
	if value.Type() == reflect.TypeFor[time.Time]() {
		if state.location != nil {
			return reflect.ValueOf(value.Interface().(time.Time).In(state.location)), nil
		}
		return value, nil
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := databaseValueVisit{typ: value.Type(), pointer: value.Pointer()}
		if value.Kind() == reflect.Slice {
			visit.length = value.Len()
		}
		if state.active[visit] {
			return reflect.Value{}, fmt.Errorf("%w: 参数包含循环引用 (%s)", ErrInvalidQuery, value.Type())
		}
		if previous, exists := state.completed[visit]; exists {
			return previous, nil
		}
		if state.active == nil {
			state.active = make(map[databaseValueVisit]bool)
			state.completed = make(map[databaseValueVisit]reflect.Value)
		}
		state.active[visit] = true
		defer func() {
			delete(state.active, visit)
			if copyErr == nil {
				state.completed[visit] = copied
			}
		}()
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		child, err := state.copy(value.Elem(), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		if value.Kind() == reflect.Pointer {
			result := reflect.New(value.Type().Elem())
			result.Elem().Set(child)
			return result.Convert(value.Type()), nil
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(child)
		return result, nil
	case reflect.Map:
		if value.Len() > state.remaining {
			return reflect.Value{}, fmt.Errorf("%w: 映射超过参数节点预算", ErrInvalidQuery)
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key()
			if !state.immutable(key, depth+1) {
				return reflect.Value{}, fmt.Errorf("%w: 映射键含可变引用 (%s)", ErrInvalidQuery, key.Type())
			}
			child, err := state.copy(iterator.Value(), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(key, child)
		}
		return result, nil
	case reflect.Slice, reflect.Array:
		// 字节数据本身没有引用边，直接复制，避免按字节消耗图遍历预算。
		binary := value.Type().Elem().Kind() == reflect.Uint8
		if !binary && value.Len() > state.remaining {
			return reflect.Value{}, fmt.Errorf("%w: 序列超过参数节点预算", ErrInvalidQuery)
		}
		var result reflect.Value
		if value.Kind() == reflect.Slice {
			result = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		} else {
			result = reflect.New(value.Type()).Elem()
		}
		if binary {
			if value.Kind() == reflect.Array {
				result.Set(value)
			} else {
				reflect.Copy(result, value)
			}
			return result, nil
		}
		for index := 0; index < value.Len(); index++ {
			child, err := state.copy(value.Index(index), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(child)
		}
		return result, nil
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath != "" {
				if !state.immutable(value.Field(index), depth+1) {
					return reflect.Value{}, fmt.Errorf("%w: 类型 %s 的私有字段 %s 无法安全快照", ErrInvalidQuery, value.Type(), field.Name)
				}
				continue
			}
			child, err := state.copy(value.Field(index), depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Field(index).Set(child)
		}
		return result, nil
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("%w: 不支持参数类型 %s", ErrInvalidQuery, value.Type())
	default:
		return value, nil
	}
}

// immutable 只允许无需访问私有状态即可证明安全的纯值，检查本身同样受预算约束。
func (state *databaseValueSnapshot) immutable(value reflect.Value, depth int) bool {
	if depth >= maxDatabaseValueDepth || state.remaining <= 0 {
		return false
	}
	state.remaining--
	if value.Type() == reflect.TypeFor[time.Time]() {
		return true
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return false
	case reflect.Interface:
		return value.IsNil() || state.immutable(value.Elem(), depth+1)
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if !state.immutable(value.Field(index), depth+1) {
				return false
			}
		}
	case reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return true
		}
		for index := 0; index < value.Len(); index++ {
			if !state.immutable(value.Index(index), depth+1) {
				return false
			}
		}
	}
	return true
}
