package db

import (
	"encoding/base64"
	"fmt"
	"math"
	"reflect"
	"time"
)

func validateImmediateRelation(current, related *Model, identifiers ...string) error {
	if current == nil || related == nil {
		return fmt.Errorf("%w: 关联模型不能为空", ErrInvalidRelation)
	}
	if err := current.validationError(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRelation, err)
	}
	if err := related.validationError(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRelation, err)
	}
	for _, identifier := range identifiers {
		if err := validateIdentifier(identifier); err != nil {
			return fmt.Errorf("%w: 非法关联标识符 %q: %w", ErrInvalidRelation, identifier, err)
		}
	}
	return nil
}

func collectRelationKeys(rows []map[string]interface{}, field string) ([]interface{}, error) {
	seen := make(map[string]bool)
	keys := make([]interface{}, 0)
	for index, row := range rows {
		value, ok := row[field]
		if !ok {
			return nil, fmt.Errorf("%w: 第 %d 行缺少关联字段 %q", ErrInvalidRelation, index, field)
		}
		key, usable, err := relationValueKey(value)
		if err != nil {
			return nil, err
		}
		if !usable || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, value)
	}
	return keys, nil
}

func groupRelationRows(rows []map[string]interface{}, field string) (map[string][]map[string]interface{}, error) {
	grouped := make(map[string][]map[string]interface{})
	for index, row := range rows {
		value, exists := row[field]
		if !exists {
			return nil, fmt.Errorf("%w: 第 %d 行缺少关联字段 %q", ErrInvalidRelation, index, field)
		}
		key, usable, err := relationValueKey(value)
		if err != nil {
			return nil, err
		}
		if usable {
			grouped[key] = append(grouped[key], row)
		}
	}
	return grouped, nil
}

func indexRelationRows(rows []map[string]interface{}, field string) (map[string]map[string]interface{}, error) {
	indexed := make(map[string]map[string]interface{})
	for index, row := range rows {
		value, exists := row[field]
		if !exists {
			return nil, fmt.Errorf("%w: 第 %d 行缺少关联字段 %q", ErrInvalidRelation, index, field)
		}
		key, usable, err := relationValueKey(value)
		if err != nil {
			return nil, err
		}
		if !usable {
			continue
		}
		if _, duplicated := indexed[key]; duplicated {
			return nil, fmt.Errorf("%w: 关联唯一键 %q 重复", ErrInvalidRelation, field)
		}
		indexed[key] = row
	}
	return indexed, nil
}

// relationValueKey 保留数据库键的具体类型，避免数字 1 与字符串 "1" 串联关联。
func relationValueKey(value interface{}) (string, bool, error) {
	if value == nil {
		return "", false, nil
	}
	if binary, ok := value.([]byte); ok {
		return "[]byte:" + base64.RawStdEncoding.EncodeToString(binary), true, nil
	}
	if timestamp, ok := value.(time.Time); ok {
		return "time.Time:" + timestamp.UTC().Format(time.RFC3339Nano), true, nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.String:
		return reflected.Type().String() + ":" + reflected.String(), true, nil
	case reflect.Bool:
		return fmt.Sprintf("%s:%t", reflected.Type(), reflected.Bool()), true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fmt.Sprintf("%s:%d", reflected.Type(), reflected.Int()), true, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return fmt.Sprintf("%s:%d", reflected.Type(), reflected.Uint()), true, nil
	case reflect.Float32, reflect.Float64:
		numeric := reflected.Float()
		if math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return "", false, fmt.Errorf("%w: 关联键不能使用非有限浮点数", ErrInvalidRelation)
		}
		return fmt.Sprintf("%s:%v", reflected.Type(), value), true, nil
	default:
		return "", false, fmt.Errorf("%w: 不支持关联键类型 %T", ErrInvalidRelation, value)
	}
}
