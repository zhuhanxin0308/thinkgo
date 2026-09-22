package db

import (
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"time"
)

// cloneDatabaseMap 复制数据库行的字段映射，避免框架追加字段时污染调用方所有权。
func cloneDatabaseMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = cloneDatabaseValue(value)
	}
	return cloned
}

// normalizeDatabaseWriteMap 将写入边界的 time.Time 统一转换到应用时区，保持绝对时刻不变。
func normalizeDatabaseWriteMap(source map[string]interface{}, location *time.Location) map[string]interface{} {
	if source == nil {
		return nil
	}
	if location == nil {
		location = time.UTC
	}
	normalized := cloneDatabaseMap(source)
	for key, value := range normalized {
		normalized[key] = normalizeDatabaseWriteValue(value, location)
	}
	return normalized
}

func normalizeDatabaseWriteValue(value interface{}, location *time.Location) interface{} {
	switch typed := value.(type) {
	case time.Time:
		return typed.In(location)
	case *time.Time:
		if typed == nil {
			return (*time.Time)(nil)
		}
		converted := typed.In(location)
		return &converted
	case map[string]interface{}:
		normalized := make(map[string]interface{}, len(typed))
		for key, nested := range typed {
			normalized[key] = normalizeDatabaseWriteValue(nested, location)
		}
		return normalized
	case []interface{}:
		normalized := make([]interface{}, len(typed))
		for index, nested := range typed {
			normalized[index] = normalizeDatabaseWriteValue(nested, location)
		}
		return normalized
	default:
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() {
			return value
		}
		switch reflected.Kind() {
		case reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.Array:
			return normalizeDatabaseWriteReflectValue(reflected, location).Interface()
		}
		return value
	}
}

// normalizeDatabaseWriteReflectValue 递归处理命名 map/slice，覆盖 bson.M、bson.A 等容器。
func normalizeDatabaseWriteReflectValue(value reflect.Value, location *time.Location) reflect.Value {
	if !value.IsValid() {
		return value
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		return reflect.ValueOf(value.Interface().(time.Time).In(location))
	}
	if value.Type() == reflect.TypeOf((*time.Time)(nil)) {
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		converted := value.Interface().(*time.Time).In(location)
		return reflect.ValueOf(&converted)
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(normalizeDatabaseWriteReflectValue(value.Elem(), location))
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(normalizeDatabaseWriteReflectValue(value.Elem(), location))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(iterator.Key(), normalizeDatabaseWriteReflectValue(iterator.Value(), location))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(normalizeDatabaseWriteReflectValue(value.Index(index), location))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(normalizeDatabaseWriteReflectValue(value.Index(index), location))
		}
		return result
	default:
		return value
	}
}

func cloneDatabaseValues(source []interface{}) []interface{} {
	return cloneDatabaseValuesWithExtraCapacity(source, 0)
}

// cloneDatabaseValuesWithExtraCapacity 深拷贝参数并为批量追加预留容量。
func cloneDatabaseValuesWithExtraCapacity(source []interface{}, extraCapacity int) []interface{} {
	if source == nil {
		if extraCapacity <= 0 {
			return nil
		}
	}
	if extraCapacity < 0 {
		extraCapacity = 0
	}
	capacity := len(source) + extraCapacity
	if capacity < len(source) {
		capacity = len(source)
	}
	cloned := make([]interface{}, len(source), capacity)
	for index, value := range source {
		cloned[index] = cloneDatabaseValue(value)
	}
	return cloned
}

// cloneDatabaseValue 复制数据库请求/结果中常见的可变容器，避免调用方在请求
// 构造后通过共享的 map、slice、array、pointer 或 []byte 修改内部状态。
func cloneDatabaseValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	return cloneDatabaseReflectValue(reflect.ValueOf(value)).Interface()
}

func cloneDatabaseReflectValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneDatabaseReflectValue(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(cloneDatabaseReflectValue(value.Elem()))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(iterator.Key(), cloneDatabaseReflectValue(iterator.Value()))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneDatabaseReflectValue(value.Index(index)))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneDatabaseReflectValue(value.Index(index)))
		}
		return result
	default:
		return value
	}
}

func cloneDatabaseRows(rows []map[string]interface{}) []map[string]interface{} {
	if rows == nil {
		return nil
	}
	cloned := make([]map[string]interface{}, len(rows))
	for index, row := range rows {
		cloned[index] = cloneDatabaseMap(row)
	}
	return cloned
}

func sortedDatabaseKeys(data map[string]interface{}) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// compareOrderedDatabaseCursor implements the framework's conservative default
// cursor contract. Only native integers and time.Time have a stable ordering the
// framework can compare without knowing a database numeric type or collation.
func compareOrderedDatabaseCursor(left, right interface{}) (int, error) {
	if left == nil || right == nil {
		return 0, fmt.Errorf("%w: 游标值不能为空", ErrInvalidDatabaseRow)
	}
	if leftInteger, ok := orderedDatabaseIntegerValue(left); ok {
		rightInteger, rightOK := orderedDatabaseIntegerValue(right)
		if !rightOK {
			return 0, fmt.Errorf("%w: 游标类型从 %T 变为 %T", ErrInvalidDatabaseRow, left, right)
		}
		return compareOrderedDatabaseIntegers(leftInteger, rightInteger), nil
	}
	if leftTime, ok := left.(time.Time); ok {
		rightTime, rightOK := right.(time.Time)
		if !rightOK {
			return 0, fmt.Errorf("%w: 游标类型从 %T 变为 %T", ErrInvalidDatabaseRow, left, right)
		}
		if leftTime.Before(rightTime) {
			return -1, nil
		}
		if leftTime.After(rightTime) {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("%w: 默认游标不支持 %T", ErrUnsupportedCursorKey, left)
}

type orderedDatabaseInteger struct {
	signed        bool
	signedValue   int64
	unsignedValue uint64
}

func orderedDatabaseIntegerValue(value interface{}) (orderedDatabaseInteger, bool) {
	switch typed := value.(type) {
	case int:
		return orderedDatabaseInteger{signed: true, signedValue: int64(typed)}, true
	case int8:
		return orderedDatabaseInteger{signed: true, signedValue: int64(typed)}, true
	case int16:
		return orderedDatabaseInteger{signed: true, signedValue: int64(typed)}, true
	case int32:
		return orderedDatabaseInteger{signed: true, signedValue: int64(typed)}, true
	case int64:
		return orderedDatabaseInteger{signed: true, signedValue: typed}, true
	case uint:
		return orderedDatabaseInteger{unsignedValue: uint64(typed)}, true
	case uint8:
		return orderedDatabaseInteger{unsignedValue: uint64(typed)}, true
	case uint16:
		return orderedDatabaseInteger{unsignedValue: uint64(typed)}, true
	case uint32:
		return orderedDatabaseInteger{unsignedValue: uint64(typed)}, true
	case uint64:
		return orderedDatabaseInteger{unsignedValue: typed}, true
	default:
		return orderedDatabaseInteger{}, false
	}
}

func compareOrderedDatabaseIntegers(left, right orderedDatabaseInteger) int {
	if left.signed && right.signed {
		return compareInt64(left.signedValue, right.signedValue)
	}
	if !left.signed && !right.signed {
		return compareUint64(left.unsignedValue, right.unsignedValue)
	}
	if left.signed {
		if left.signedValue < 0 {
			return -1
		}
		return compareUint64(uint64(left.signedValue), right.unsignedValue)
	}
	if right.signedValue < 0 {
		return 1
	}
	return compareUint64(left.unsignedValue, uint64(right.signedValue))
}

func compareInt64(left, right int64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareUint64(left, right uint64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func databaseInteger(value interface{}) (*big.Int, bool) {
	integer, ok := orderedDatabaseIntegerValue(value)
	if !ok {
		return nil, false
	}
	result := new(big.Int)
	if integer.signed {
		return result.SetInt64(integer.signedValue), true
	}
	return result.SetUint64(integer.unsignedValue), true
}
