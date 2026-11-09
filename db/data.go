package db

import (
	"fmt"
	"math/big"
	"sort"
	"time"
)

// cloneDatabaseMap 复制数据库行的字段映射，避免框架追加字段时污染调用方所有权。
func cloneDatabaseMap(source map[string]interface{}) (map[string]interface{}, error) {
	if source == nil {
		return nil, nil
	}
	cloned, err := cloneDatabaseValue(source)
	if err != nil {
		return nil, err
	}
	return cloned.(map[string]interface{}), nil
}

// normalizeDatabaseWriteMap 将写入边界的 time.Time 统一转换到应用时区，保持绝对时刻不变。
func normalizeDatabaseWriteMap(source map[string]interface{}, location *time.Location) (map[string]interface{}, error) {
	if source == nil {
		return nil, nil
	}
	if location == nil {
		location = time.UTC
	}
	normalized, err := snapshotDatabaseValue(source, location)
	if err != nil {
		return nil, err
	}
	return normalized.(map[string]interface{}), nil
}

func cloneDatabaseValues(source []interface{}) ([]interface{}, error) {
	return cloneDatabaseValuesWithExtraCapacity(source, 0)
}

// cloneDatabaseValuesWithExtraCapacity 深拷贝参数并为批量追加预留容量。
func cloneDatabaseValuesWithExtraCapacity(source []interface{}, extraCapacity int) ([]interface{}, error) {
	if source == nil {
		if extraCapacity <= 0 {
			return nil, nil
		}
	}
	if extraCapacity < 0 {
		extraCapacity = 0
	}
	capacity := len(source) + extraCapacity
	if capacity < len(source) {
		capacity = len(source)
	}
	if len(source) > maxDatabaseValueNodes {
		return nil, fmt.Errorf("%w: 参数列表超过节点预算", ErrInvalidQuery)
	}
	cloned := make([]interface{}, len(source), capacity)
	state := databaseValueSnapshot{remaining: maxDatabaseValueNodes}
	for index, value := range source {
		var err error
		cloned[index], err = state.snapshot(value)
		if err != nil {
			return nil, err
		}
	}
	return cloned, nil
}

// cloneDatabaseValue 复制数据库请求/结果中常见的可变容器，避免调用方在请求
// 构造后通过共享的 map、slice、array、pointer 或 []byte 修改内部状态。
func cloneDatabaseValue(value interface{}) (interface{}, error) {
	return snapshotDatabaseValue(value, nil)
}

func cloneDatabaseRows(rows []map[string]interface{}) ([]map[string]interface{}, error) {
	if rows == nil {
		return nil, nil
	}
	cloned := make([]map[string]interface{}, len(rows))
	for index, row := range rows {
		var err error
		cloned[index], err = cloneDatabaseMap(row)
		if err != nil {
			return nil, err
		}
	}
	return cloned, nil
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
