package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SQLConnection 封装 database/sql 与方言构建器。
type SQLConnection struct {
	DB      *sql.DB
	Builder Builder
}

var (
	_ Connection             = (*SQLConnection)(nil)
	_ RawQueryable           = (*SQLConnection)(nil)
	_ ContextualConnection   = (*SQLConnection)(nil)
	_ ContextualRawQueryable = (*SQLConnection)(nil)
)

func (c *SQLConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return c.SelectContext(context.Background(), table, fields, where, args, order, limit, offset)
}

func (c *SQLConnection) SelectContext(ctx context.Context, table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	if err := c.validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validateSelectArguments(table, fields, where, args, order, limit, offset); err != nil {
		return nil, err
	}
	query := c.Builder.Rebind(c.Builder.Select(table, fields, where, order, limit, offset))
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func (c *SQLConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return c.InsertContext(context.Background(), table, data)
}

func (c *SQLConnection) InsertContext(ctx context.Context, table string, data map[string]interface{}) (int64, error) {
	return c.InsertContextWithPrimaryKey(ctx, table, data, "id")
}

// InsertContextWithPrimaryKey 为不支持 LastInsertId 的方言传递模型真实主键。
func (c *SQLConnection) InsertContextWithPrimaryKey(ctx context.Context, table string, data map[string]interface{}, primaryKey string) (int64, error) {
	if err := c.validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateWriteArguments(table, data); err != nil {
		return 0, err
	}
	if err := validateIdentifier(primaryKey); err != nil {
		return 0, fmt.Errorf("%w: 非法回传主键: %w", ErrInvalidQuery, err)
	}
	if !c.Builder.SupportsLastInsertId() {
		if query, values, ok := c.Builder.InsertReturning(table, data, primaryKey); ok {
			if len(values) > 0 {
				if output, isOutput := values[len(values)-1].(sql.Out); isOutput {
					if _, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), values...); err != nil {
						return 0, err
					}
					identifier, ok := output.Dest.(*int64)
					if !ok || identifier == nil {
						return 0, fmt.Errorf("%w: RETURNING 输出目标必须是 *int64", ErrInvalidDatabaseRow)
					}
					return *identifier, nil
				}
			}
			var id int64
			if err := c.DB.QueryRowContext(ctx, c.Builder.Rebind(query), values...).Scan(&id); err != nil {
				return 0, err
			}
			return id, nil
		}
		return 0, fmt.Errorf("%w: 当前方言不支持返回插入主键", ErrInvalidQuery)
	}

	query, values := c.Builder.Insert(table, data)
	result, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), values...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (c *SQLConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return c.UpdateContext(context.Background(), table, data, where, args)
}

func (c *SQLConnection) UpdateContext(ctx context.Context, table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	if err := c.validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateWriteArguments(table, data); err != nil {
		return 0, err
	}
	if err := validateMutationPredicate(where, args); err != nil {
		return 0, err
	}
	query, values := c.Builder.Update(table, data, where)
	values = append(values, args...)
	result, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), values...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (c *SQLConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return c.DeleteContext(context.Background(), table, where, args)
}

func (c *SQLConnection) DeleteContext(ctx context.Context, table string, where []string, args []interface{}) (int64, error) {
	if err := c.validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateIdentifier(table); err != nil {
		return 0, fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	if err := validateMutationPredicate(where, args); err != nil {
		return 0, err
	}
	query := c.Builder.Delete(table, where)
	result, err := c.DB.ExecContext(ctx, c.Builder.Rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (c *SQLConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return c.CountContext(context.Background(), table, where, args)
}

func (c *SQLConnection) CountContext(ctx context.Context, table string, where []string, args []interface{}) (int64, error) {
	if err := c.validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateIdentifier(table); err != nil {
		return 0, fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	if err := validatePlaceholderCount(strings.Join(where, " AND "), len(args)); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	query := c.Builder.Count(table, where)
	var count int64
	err := c.DB.QueryRowContext(ctx, c.Builder.Rebind(query), args...).Scan(&count)
	return count, err
}

func (c *SQLConnection) Close() error {
	if c == nil || c.DB == nil {
		return ErrDatabaseUnavailable
	}
	return c.DB.Close()
}

func (c *SQLConnection) Query(rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	return c.QueryContext(context.Background(), rawSQL, args...)
}

func (c *SQLConnection) QueryContext(ctx context.Context, rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	if err := c.validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx, c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func (c *SQLConnection) Execute(rawSQL string, args ...interface{}) (int64, error) {
	return c.ExecuteContext(context.Background(), rawSQL, args...)
}

func (c *SQLConnection) ExecuteContext(ctx context.Context, rawSQL string, args ...interface{}) (int64, error) {
	if err := c.validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return 0, err
	}
	result, err := c.DB.ExecContext(ctx, c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (c *SQLConnection) validateContext(ctx context.Context) error {
	if c == nil || c.DB == nil || isNilDatabaseDependency(c.Builder) {
		return ErrDatabaseUnavailable
	}
	if ctx == nil {
		return fmt.Errorf("%w: SQL 上下文不能为空", ErrInvalidQuery)
	}
	return nil
}

func validateSelectArguments(table, fields string, where []string, args []interface{}, order string, limit, offset int) error {
	if err := validateIdentifier(table); err != nil {
		return fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	if err := validateIdentifierList(fields); err != nil {
		return fmt.Errorf("%w: 非法字段列表: %w", ErrInvalidQuery, err)
	}
	if err := validateOrderClause(order); err != nil {
		return fmt.Errorf("%w: 非法排序: %w", ErrInvalidQuery, err)
	}
	if limit < 0 || offset < 0 {
		return fmt.Errorf("%w: limit 或 offset 不能为负数", ErrInvalidPagination)
	}
	if err := validatePlaceholderCount(strings.Join(where, " AND "), len(args)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

func validateWriteArguments(table string, data map[string]interface{}) error {
	if err := validateIdentifier(table); err != nil {
		return fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: 写入数据不能为空", ErrInvalidQuery)
	}
	if err := validateDataKeys(data); err != nil {
		return fmt.Errorf("%w: 非法写入字段: %w", ErrInvalidQuery, err)
	}
	return nil
}

func validateMutationPredicate(where []string, args []interface{}) error {
	if len(where) == 0 {
		return ErrUnsafeFullTableMutation
	}
	if err := validatePlaceholderCount(strings.Join(where, " AND "), len(args)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

// scanRows 接管结果集生命周期，并保持二进制列的 []byte 类型。
func scanRows(rows *sql.Rows) (results []map[string]interface{}, resultErr error) {
	if rows == nil {
		return nil, fmt.Errorf("%w: 结果集不能为空", ErrInvalidDatabaseRow)
	}
	defer func() {
		resultErr = errors.Join(resultErr, rows.Close())
	}()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	seenColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, duplicated := seenColumns[column]; duplicated {
			return nil, fmt.Errorf("%w: 结果包含重复列 %q，请使用唯一别名", ErrInvalidDatabaseRow, column)
		}
		seenColumns[column] = struct{}{}
	}

	results = make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, len(columns))
		scanArgs := make([]interface{}, len(columns))
		for index := range values {
			scanArgs[index] = &values[index]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}
		entry := make(map[string]interface{}, len(columns))
		for index, column := range columns {
			if binary, ok := values[index].([]byte); ok {
				entry[column] = append([]byte(nil), binary...)
			} else {
				entry[column] = values[index]
			}
		}
		results = append(results, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
