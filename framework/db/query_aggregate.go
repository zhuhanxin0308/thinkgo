package db

import (
	"fmt"
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
	cloned.fields = fmt.Sprintf("%s(%s) AS tp_aggregate", fn, field)

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
		return v, nil
	case float32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case []byte:
		var f float64
		fmt.Sscanf(string(v), "%f", &f)
		return f, nil
	case string:
		var f float64
		fmt.Sscanf(v, "%f", &f)
		return f, nil
	}

	return 0, fmt.Errorf("unexpected aggregate value type: %T", val)
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

	q.fields = field
	rows, err := q.Select()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0][field], nil
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

	if len(key) > 0 && key[0] != "" {
		if err := validateIdentifier(key[0]); err != nil {
			return nil, q.reportError("column", fmt.Errorf("unsafe key field name: %w", err), nil)
		}
		q.fields = field + ", " + key[0]
	} else {
		q.fields = field
	}

	rows, err := q.Select()
	if err != nil {
		return nil, err
	}

	// 带 key 时返回 map[string]interface{}
	if len(key) > 0 && key[0] != "" {
		result := make(map[string]interface{}, len(rows))
		for _, row := range rows {
			keyVal := fmt.Sprintf("%v", row[key[0]])
			result[keyVal] = row[field]
		}
		return result, nil
	}

	// 无 key 时返回 []interface{}
	result := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		result = append(result, row[field])
	}
	return result, nil
}
