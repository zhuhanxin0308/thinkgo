package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
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
		cloned[key] = value
	}
	return cloned
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

// compareDatabaseCursor 比较游标值，支持驱动常见的整数、浮点、文本、字节和时间类型。
func compareDatabaseCursor(left, right interface{}) (int, error) {
	if left == nil || right == nil {
		return 0, fmt.Errorf("%w: 游标值不能为空", ErrInvalidDatabaseRow)
	}
	if leftNumber, ok := databaseNumber(left); ok {
		rightNumber, rightOK := databaseNumber(right)
		if !rightOK {
			return 0, fmt.Errorf("%w: 游标类型从 %T 变为 %T", ErrInvalidDatabaseRow, left, right)
		}
		return leftNumber.Cmp(rightNumber), nil
	}
	switch typedLeft := left.(type) {
	case string:
		typedRight, ok := right.(string)
		if !ok {
			return 0, fmt.Errorf("%w: 游标类型从 %T 变为 %T", ErrInvalidDatabaseRow, left, right)
		}
		if typedLeft < typedRight {
			return -1, nil
		}
		if typedLeft > typedRight {
			return 1, nil
		}
		return 0, nil
	case []byte:
		typedRight, ok := right.([]byte)
		if !ok {
			return 0, fmt.Errorf("%w: 游标类型从 %T 变为 %T", ErrInvalidDatabaseRow, left, right)
		}
		return bytes.Compare(typedLeft, typedRight), nil
	case time.Time:
		typedRight, ok := right.(time.Time)
		if !ok {
			return 0, fmt.Errorf("%w: 游标类型从 %T 变为 %T", ErrInvalidDatabaseRow, left, right)
		}
		if typedLeft.Before(typedRight) {
			return -1, nil
		}
		if typedLeft.After(typedRight) {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("%w: 不支持游标类型 %T", ErrInvalidDatabaseRow, left)
	}
}

func databaseNumber(value interface{}) (*big.Rat, bool) {
	result := new(big.Rat)
	switch typed := value.(type) {
	case int:
		return result.SetInt64(int64(typed)), true
	case int8:
		return result.SetInt64(int64(typed)), true
	case int16:
		return result.SetInt64(int64(typed)), true
	case int32:
		return result.SetInt64(int64(typed)), true
	case int64:
		return result.SetInt64(typed), true
	case uint:
		return result.SetInt(new(big.Int).SetUint64(uint64(typed))), true
	case uint8:
		return result.SetInt64(int64(typed)), true
	case uint16:
		return result.SetInt64(int64(typed)), true
	case uint32:
		return result.SetInt64(int64(typed)), true
	case uint64:
		return result.SetInt(new(big.Int).SetUint64(typed)), true
	case float32:
		parsed := result.SetFloat64(float64(typed))
		return parsed, parsed != nil
	case float64:
		parsed := result.SetFloat64(typed)
		return parsed, parsed != nil
	case json.Number:
		parsed, ok := result.SetString(string(typed))
		return parsed, ok
	default:
		return nil, false
	}
}
