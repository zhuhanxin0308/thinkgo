package db

import (
	"encoding/base64"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

// relationRow keeps database keys separate from the getter-transformed data
// exposed to callers. Eager-loading must always join on raw database values.
type relationRow struct {
	data    map[string]interface{}
	rawKeys map[string]interface{}
}

// relationComparableKey 是内部关联索引使用的可比较键接口，调用方只接收本函数返回的可比较值。
type relationComparableKey = interface{}

type relationBytesKey string
type relationTimeKey string

type relationFloatKey struct {
	typeName string
	text     string
}

func makeRelationRows(rows []map[string]interface{}, keyFields []string, model *Model) ([]relationRow, error) {
	result := make([]relationRow, len(rows))
	for index, row := range rows {
		rawKeys := make(map[string]interface{}, len(keyFields))
		for _, field := range keyFields {
			value, exists := row[field]
			if !exists {
				return nil, fmt.Errorf("%w: 第 %d 行缺少关联字段 %q", ErrInvalidRelation, index, field)
			}
			rawKeys[field] = cloneDatabaseValue(value)
		}
		data := cloneDatabaseMap(row)
		if model != nil {
			data = model.applyGetters(data)
		}
		result[index] = relationRow{data: data, rawKeys: rawKeys}
	}
	return result, nil
}

func selectRelationRows(query *ModelQuery, keyFields ...string) ([]relationRow, error) {
	if err := query.validationError(); err != nil {
		return nil, err
	}
	rows, err := query.prepareQuery().Select()
	if err != nil {
		return nil, err
	}
	return makeRelationRows(rows, keyFields, query.model)
}

func groupRawRelationRows(rows []relationRow, field string) (map[relationComparableKey][]map[string]interface{}, error) {
	grouped := make(map[relationComparableKey][]map[string]interface{})
	for index, row := range rows {
		value, exists := row.rawKeys[field]
		if !exists {
			return nil, fmt.Errorf("%w: 第 %d 行缺少原始关联字段 %q", ErrInvalidRelation, index, field)
		}
		key, usable, err := relationComparableValueKey(value)
		if err != nil {
			return nil, err
		}
		if usable {
			grouped[key] = append(grouped[key], row.data)
		}
	}
	return grouped, nil
}

func indexRawRelationRows(rows []relationRow, field string) (map[relationComparableKey]map[string]interface{}, error) {
	indexed := make(map[relationComparableKey]map[string]interface{})
	for index, row := range rows {
		value, exists := row.rawKeys[field]
		if !exists {
			return nil, fmt.Errorf("%w: 第 %d 行缺少原始关联字段 %q", ErrInvalidRelation, index, field)
		}
		key, usable, err := relationComparableValueKey(value)
		if err != nil {
			return nil, err
		}
		if !usable {
			continue
		}
		if _, duplicated := indexed[key]; duplicated {
			return nil, fmt.Errorf("%w: 关联唯一键 %q 重复", ErrInvalidRelation, field)
		}
		indexed[key] = row.data
	}
	return indexed, nil
}

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
	seen := make(map[relationComparableKey]bool)
	keys := make([]interface{}, 0)
	for index, row := range rows {
		value, ok := row[field]
		if !ok {
			return nil, fmt.Errorf("%w: 第 %d 行缺少关联字段 %q", ErrInvalidRelation, index, field)
		}
		key, usable, err := relationComparableValueKey(value)
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

// relationFloatValueKey 统一校验并格式化关系键中的有限浮点数。
func relationFloatValueKey(typeName string, value float64, bitSize int) (string, bool, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", false, fmt.Errorf("%w: 关联键不能使用非有限浮点数", ErrInvalidRelation)
	}
	return typeName + ":" + strconv.FormatFloat(value, 'g', -1, bitSize), true, nil
}

// relationComparableValueKey 构造内部关联索引键，整数直接保留数值避免字符串分配。
func relationComparableValueKey(value interface{}) (relationComparableKey, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	if binary, ok := value.([]byte); ok {
		return relationBytesKey(string(binary)), true, nil
	}
	if timestamp, ok := value.(time.Time); ok {
		return relationTimeKey(timestamp.UTC().Format(time.RFC3339Nano)), true, nil
	}
	switch typed := value.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return typed, true, nil
	case float32:
		key, _, err := relationFloatValueKey("float32", float64(typed), 32)
		if err != nil {
			return nil, false, err
		}
		return relationFloatKey{typeName: "float32", text: key}, true, nil
	case float64:
		key, _, err := relationFloatValueKey("float64", typed, 64)
		if err != nil {
			return nil, false, err
		}
		return relationFloatKey{typeName: "float64", text: key}, true, nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value, true, nil
	case reflect.Float32, reflect.Float64:
		key, _, err := relationFloatValueKey(reflected.Type().String(), reflected.Float(), reflected.Type().Bits())
		if err != nil {
			return nil, false, err
		}
		return relationFloatKey{typeName: reflected.Type().String(), text: key}, true, nil
	default:
		return nil, false, fmt.Errorf("%w: 不支持关联键类型 %T", ErrInvalidRelation, value)
	}
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
	switch typed := value.(type) {
	case string:
		return "string:" + typed, true, nil
	case bool:
		return "bool:" + strconv.FormatBool(typed), true, nil
	case int:
		return "int:" + strconv.Itoa(typed), true, nil
	case int8:
		return "int8:" + strconv.FormatInt(int64(typed), 10), true, nil
	case int16:
		return "int16:" + strconv.FormatInt(int64(typed), 10), true, nil
	case int32:
		return "int32:" + strconv.FormatInt(int64(typed), 10), true, nil
	case int64:
		return "int64:" + strconv.FormatInt(typed, 10), true, nil
	case uint:
		return "uint:" + strconv.FormatUint(uint64(typed), 10), true, nil
	case uint8:
		return "uint8:" + strconv.FormatUint(uint64(typed), 10), true, nil
	case uint16:
		return "uint16:" + strconv.FormatUint(uint64(typed), 10), true, nil
	case uint32:
		return "uint32:" + strconv.FormatUint(uint64(typed), 10), true, nil
	case uint64:
		return "uint64:" + strconv.FormatUint(typed, 10), true, nil
	case float32:
		return relationFloatValueKey("float32", float64(typed), 32)
	case float64:
		return relationFloatValueKey("float64", typed, 64)
	}
	reflected := reflect.ValueOf(value)
	typeName := reflected.Type().String()
	switch reflected.Kind() {
	case reflect.String:
		return typeName + ":" + reflected.String(), true, nil
	case reflect.Bool:
		return typeName + ":" + strconv.FormatBool(reflected.Bool()), true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return typeName + ":" + strconv.FormatInt(reflected.Int(), 10), true, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return typeName + ":" + strconv.FormatUint(reflected.Uint(), 10), true, nil
	case reflect.Float32, reflect.Float64:
		numeric := reflected.Float()
		if math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return "", false, fmt.Errorf("%w: 关联键不能使用非有限浮点数", ErrInvalidRelation)
		}
		return typeName + ":" + strconv.FormatFloat(numeric, 'g', -1, reflected.Type().Bits()), true, nil
	default:
		return "", false, fmt.Errorf("%w: 不支持关联键类型 %T", ErrInvalidRelation, value)
	}
}
