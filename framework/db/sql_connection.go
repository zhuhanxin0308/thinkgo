package db

import (
	"context"
	"database/sql"
)

// SQLConnection SQL 数据库连接实现
type SQLConnection struct {
	DB      *sql.DB
	Builder Builder
}

// 编译期断言：SQLConnection 实现 context 版接口。
var (
	_ Connection            = (*SQLConnection)(nil)
	_ RawQueryable          = (*SQLConnection)(nil)
	_ ContextualConnection  = (*SQLConnection)(nil)
	_ ContextualRawQueryable = (*SQLConnection)(nil)
)

func (c *SQLConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return c.SelectContext(context.Background(), table, fields, where, args, order, limit, offset)
}

func (c *SQLConnection) SelectContext(ctx context.Context, table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	query := c.Builder.Rebind(c.Builder.Select(table, fields, where, order, limit, offset))
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func (c *SQLConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return c.InsertContext(context.Background(), table, data)
}

func (c *SQLConnection) InsertContext(ctx context.Context, table string, data map[string]interface{}) (int64, error) {
	// 不支持 LastInsertId 的方言（如 PostgreSQL/lib/pq）改用 INSERT ... RETURNING 主键。
	if !c.Builder.SupportsLastInsertId() {
		if query, values, ok := c.Builder.InsertReturning(table, data, "id"); ok {
			var id int64
			if err := c.DB.QueryRowContext(ctx, c.Builder.Rebind(query), values...).Scan(&id); err != nil {
				return 0, err
			}
			return id, nil
		}
	}

	query, values := c.Builder.Insert(table, data)
	res, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), values...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (c *SQLConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return c.UpdateContext(context.Background(), table, data, where, args)
}

func (c *SQLConnection) UpdateContext(ctx context.Context, table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	query, values := c.Builder.Update(table, data, where)
	values = append(values, args...)
	res, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), values...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (c *SQLConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return c.DeleteContext(context.Background(), table, where, args)
}

func (c *SQLConnection) DeleteContext(ctx context.Context, table string, where []string, args []interface{}) (int64, error) {
	query := c.Builder.Delete(table, where)
	res, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (c *SQLConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return c.CountContext(context.Background(), table, where, args)
}

func (c *SQLConnection) CountContext(ctx context.Context, table string, where []string, args []interface{}) (int64, error) {
	query := c.Builder.Count(table, where)
	var count int64
	err := c.DB.QueryRowContext(ctx, c.Builder.Rebind(query), args...).Scan(&count)
	return count, err
}

func (c *SQLConnection) Close() error {
	return c.DB.Close()
}

// Query 执行原生 SQL 查询（实现 RawQueryable 接口）。
// 传入的 SQL 统一使用 ? 占位符，内部按方言转换后执行。
func (c *SQLConnection) Query(rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	return c.QueryContext(context.Background(), rawSQL, args...)
}

func (c *SQLConnection) QueryContext(ctx context.Context, rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := c.DB.QueryContext(ctx, c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// Execute 执行原生 SQL 命令（实现 RawQueryable 接口）。
// 传入的 SQL 统一使用 ? 占位符，内部按方言转换后执行。
func (c *SQLConnection) Execute(rawSQL string, args ...interface{}) (int64, error) {
	return c.ExecuteContext(context.Background(), rawSQL, args...)
}

func (c *SQLConnection) ExecuteContext(ctx context.Context, rawSQL string, args ...interface{}) (int64, error) {
	res, err := c.DB.ExecContext(ctx, c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// scanRows 将结果集逐行扫描为 map 切片，统一把 []byte 文本转换为 string。
func scanRows(rows *sql.Rows) ([]map[string]interface{}, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	count := len(columns)
	values := make([]interface{}, count)
	scanArgs := make([]interface{}, count)
	for i := range values {
		scanArgs[i] = &values[i]
	}

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}
		entry := make(map[string]interface{}, count)
		for i, col := range columns {
			if b, ok := values[i].([]byte); ok {
				entry[col] = string(b)
			} else {
				entry[col] = values[i]
			}
		}
		results = append(results, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
