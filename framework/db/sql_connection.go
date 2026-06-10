package db

import (
	"database/sql"
)

// SQLConnection SQL 数据库连接实现
type SQLConnection struct {
	DB      *sql.DB
	Builder Builder
}

func (c *SQLConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	query := c.Builder.Rebind(c.Builder.Select(table, fields, where, order, limit, offset))
	rows, err := c.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, _ := rows.Columns()
	count := len(columns)
	values := make([]interface{}, count)
	scanArgs := make([]interface{}, count)
	for i := range values {
		scanArgs[i] = &values[i]
	}

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		err := rows.Scan(scanArgs...)
		if err != nil {
			return nil, err
		}

		entry := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			b, ok := val.([]byte)
			if ok {
				entry[col] = string(b)
			} else {
				entry[col] = val
			}
		}
		results = append(results, entry)
	}
	return results, nil
}

func (c *SQLConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	query, values := c.Builder.Insert(table, data)
	res, err := c.DB.Exec(c.Builder.Rebind(query), values...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (c *SQLConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	query, values := c.Builder.Update(table, data, where)
	values = append(values, args...)
	res, err := c.DB.Exec(c.Builder.Rebind(query), values...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (c *SQLConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	query := c.Builder.Delete(table, where)
	res, err := c.DB.Exec(c.Builder.Rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (c *SQLConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	query := c.Builder.Count(table, where)
	var count int64
	err := c.DB.QueryRow(c.Builder.Rebind(query), args...).Scan(&count)
	return count, err
}

func (c *SQLConnection) Close() error {
	return c.DB.Close()
}

// Query 执行原生 SQL 查询（实现 RawQueryable 接口）。
// 传入的 SQL 统一使用 ? 占位符，内部按方言转换后执行。
func (c *SQLConnection) Query(rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := c.DB.Query(c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, _ := rows.Columns()
	count := len(columns)

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, count)
		scanArgs := make([]interface{}, count)
		for i := range values {
			scanArgs[i] = &values[i]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}

		entry := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				entry[col] = string(b)
			} else {
				entry[col] = val
			}
		}
		results = append(results, entry)
	}
	return results, rows.Err()
}

// Execute 执行原生 SQL 命令（实现 RawQueryable 接口）。
// 传入的 SQL 统一使用 ? 占位符，内部按方言转换后执行。
func (c *SQLConnection) Execute(rawSQL string, args ...interface{}) (int64, error) {
	res, err := c.DB.Exec(c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
