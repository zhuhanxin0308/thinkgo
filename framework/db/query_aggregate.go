package db

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Sum 计算字段求和。
func (q *Query) Sum(field string) (float64, error) {
	return q.aggregate("SUM", field)
}

// Avg 计算字段平均值。
func (q *Query) Avg(field string) (float64, error) {
	return q.aggregate("AVG", field)
}

// Max 计算字段最大值。
func (q *Query) Max(field string) (float64, error) {
	return q.aggregate("MAX", field)
}

// Min 计算字段最小值。
func (q *Query) Min(field string) (float64, error) {
	return q.aggregate("MIN", field)
}

// aggregate 执行底层的聚合查询操作。
func (q *Query) aggregate(fn, field string) (float64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("aggregate", err, nil)
	}
	if err := validateIdentifier(field); err != nil {
		return 0, q.reportError("aggregate", fmt.Errorf("unsafe aggregate field: %w", err), nil)
	}

	cloned := q.clone()
	cloned.aggregateExpression = &AggregateExpression{Function: fn, Field: field, Alias: "tp_aggregate"}

	rows, err := cloned.Select()
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	val := rows[0]["tp_aggregate"]
	if val == nil {
		return 0, nil
	}

	switch v := val.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			break
		}
		return v, nil
	case float32:
		value := float64(v)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			break
		}
		return value, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int8:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint8:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case []byte:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(string(v)), 64)
		if err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed, nil
		}
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed, nil
		}
	case json.Number:
		parsed, err := strconv.ParseFloat(string(v), 64)
		if err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed, nil
		}
	}

	return 0, fmt.Errorf("%w: 无法解析 %T(%v)", ErrInvalidAggregateValue, val, val)
}

// Value 获取单条记录的单个字段值。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->value('name')
func (q *Query) Value(field string) (interface{}, error) {
	if err := q.ensureValid(); err != nil {
		return nil, q.reportError("value", err, nil)
	}
	if err := validateIdentifier(field); err != nil {
		return nil, q.reportError("value", fmt.Errorf("unsafe field name: %w", err), nil)
	}

	cloned := q.clone()
	cloned.fields = field
	cloned.limit = 1
	rows, err := cloned.Select()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	value, exists := databaseResultField(rows[0], field)
	if !exists {
		return nil, q.reportError("value", fmt.Errorf("%w: 结果缺少字段 %q", ErrInvalidDatabaseRow, field), nil)
	}
	return value, nil
}

// Column 获取某个字段的所有值列表。
// 对应 ThinkPHP 的 Db::name('user')->where('status', 1)->column('name')
// 可选传入 key 字段作为返回 map 的键。
func (q *Query) Column(field string, key ...string) (interface{}, error) {
	if err := q.ensureValid(); err != nil {
		return nil, q.reportError("column", err, nil)
	}
	if err := validateIdentifier(field); err != nil {
		return nil, q.reportError("column", fmt.Errorf("unsafe field name: %w", err), nil)
	}
	if len(key) > 1 {
		return nil, q.reportError("column", fmt.Errorf("%w: Column 最多接收一个 key 字段", ErrInvalidQuery), nil)
	}

	cloned := q.clone()
	if len(key) > 0 && key[0] != "" {
		if err := validateIdentifier(key[0]); err != nil {
			return nil, q.reportError("column", fmt.Errorf("unsafe key field name: %w", err), nil)
		}
		cloned.fields = field + ", " + key[0]
	} else {
		cloned.fields = field
	}
	if cloned.canUseDirectSQLColumn() {
		keyField := ""
		if len(key) > 0 {
			keyField = key[0]
		}
		return cloned.columnFromSQL(field, keyField)
	}
	if cloned.canStreamSQLRows() {
		return cloned.columnFromStream(field, key...)
	}

	rows, err := cloned.Select()
	if err != nil {
		return nil, err
	}

	// 带 key 时返回 map[string]interface{}
	if len(key) > 0 && key[0] != "" {
		result := make(map[string]interface{}, len(rows))
		for index, row := range rows {
			keyValue, keyExists := databaseResultField(row, key[0])
			fieldValue, fieldExists := databaseResultField(row, field)
			if !keyExists || keyValue == nil || !fieldExists {
				return nil, q.reportError("column", fmt.Errorf("%w: 第 %d 行缺少 key 或目标字段", ErrInvalidDatabaseRow, index), nil)
			}
			keyText := fmt.Sprint(keyValue)
			if _, duplicated := result[keyText]; duplicated {
				return nil, q.reportError("column", fmt.Errorf("%w: key %q 重复或字符串化后冲突", ErrInvalidDatabaseRow, keyText), nil)
			}
			result[keyText] = fieldValue
		}
		return result, nil
	}

	// 无 key 时返回 []interface{}
	result := make([]interface{}, 0, len(rows))
	for index, row := range rows {
		value, exists := databaseResultField(row, field)
		if !exists {
			return nil, q.reportError("column", fmt.Errorf("%w: 第 %d 行缺少字段 %q", ErrInvalidDatabaseRow, index, field), nil)
		}
		result = append(result, value)
	}
	return result, nil
}

// canUseDirectSQLColumn 判断是否可以绕过整行 map 物化，使用 SQL 专用列扫描路径。
func (q *Query) canUseDirectSQLColumn() bool {
	return q != nil && q.txExecutor == nil && !q.needsRawSelect() && q.canStreamSQLRows()
}

// columnFromSQL 使用结构化查询参数执行专用列扫描，保持普通 SELECT 的条件和分页语义。
func (q *Query) columnFromSQL(field, key string) (interface{}, error) {
	connection, release, err := q.db.acquireConnection()
	if err != nil {
		return nil, err
	}
	defer release()
	sqlConnection, ok := connection.(*SQLConnection)
	if !ok || sqlConnection == nil {
		return nil, fmt.Errorf("%w: 直接列扫描要求 SQL 连接", ErrUnsupportedFeature)
	}
	predicate, err := q.operationPredicate()
	if err != nil {
		return nil, err
	}
	return sqlConnection.selectColumn(q.context(), newSelectRequest(
		q.resolveTable(), q.fields, predicate, q.insertPrimaryKey, q.order, q.limit, q.offset, q.aggregateExpression, q.modelPrimaryKey,
	), field, key)
}

// canStreamSQLRows 判断当前查询是否可以使用 SQL 行流，避免为非 SQL 连接改变原有结果路径。
func (q *Query) canStreamSQLRows() bool {
	if q == nil {
		return false
	}
	if q.txExecutor != nil {
		return true
	}
	if q.db == nil {
		return false
	}
	q.db.mu.RLock()
	managed := q.db.connection
	q.db.mu.RUnlock()
	if managed == nil || isNilDatabaseDependency(managed.connection) {
		return false
	}
	_, ok := managed.connection.(*SQLConnection)
	return ok
}

// columnFromStream 逐行提取 Column 结果，只保留调用方要求的值而不保存完整行集合。
func (q *Query) columnFromStream(field string, key ...string) (interface{}, error) {
	if len(key) > 0 && key[0] != "" {
		result := make(map[string]interface{})
		var callbackErr error
		index := 0
		err := q.Each(func(row map[string]interface{}) bool {
			keyValue, keyExists := databaseResultField(row, key[0])
			fieldValue, fieldExists := databaseResultField(row, field)
			if !keyExists || keyValue == nil || !fieldExists {
				callbackErr = fmt.Errorf("%w: 第 %d 行缺少 key 或目标字段", ErrInvalidDatabaseRow, index)
				return false
			}
			keyText := fmt.Sprint(keyValue)
			if _, duplicated := result[keyText]; duplicated {
				callbackErr = fmt.Errorf("%w: key %q 重复或字符串化后冲突", ErrInvalidDatabaseRow, keyText)
				return false
			}
			result[keyText] = fieldValue
			index++
			return true
		})
		if err != nil {
			return nil, err
		}
		if callbackErr != nil {
			return nil, q.reportError("column", callbackErr, nil)
		}
		return result, nil
	}

	result := make([]interface{}, 0)
	var callbackErr error
	index := 0
	err := q.Each(func(row map[string]interface{}) bool {
		value, exists := databaseResultField(row, field)
		if !exists {
			callbackErr = fmt.Errorf("%w: 第 %d 行缺少字段 %q", ErrInvalidDatabaseRow, index, field)
			return false
		}
		result = append(result, value)
		index++
		return true
	})
	if err != nil {
		return nil, err
	}
	if callbackErr != nil {
		return nil, q.reportError("column", callbackErr, nil)
	}
	return result, nil
}

// databaseResultField 优先读取完整字段名，并兼容 SQL 驱动把限定字段
// table.column 的结果列名返回为末段 column 的行为。
func databaseResultField(row map[string]interface{}, field string) (interface{}, bool) {
	if value, exists := row[field]; exists {
		return value, true
	}
	separator := strings.LastIndexByte(field, '.')
	if separator < 0 || separator == len(field)-1 {
		return nil, false
	}
	value, exists := row[field[separator+1:]]
	return value, exists
}
