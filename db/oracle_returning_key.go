package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/db/internal/contract"
)

type oracleTypeQuery func(context.Context, string, ...any) *sql.Row

func (c *SQLConnection) insertOracleReturning(ctx context.Context, transaction *sql.Tx, request InsertRequest, data map[string]any) (InsertResult, error) {
	primary := request.PrimaryKey()
	query, values, ok := c.Builder.InsertReturning(request.Table(), data, primary)
	if !ok || len(values) == 0 {
		return InsertResult{}, ErrInsertIDUnavailable
	}
	last := len(values) - 1
	output, ok := values[last].(sql.Out)
	if !ok {
		return InsertResult{}, ErrInsertIDUnavailable
	}

	execute := c.execContext
	var queryType oracleTypeQuery
	if transaction != nil {
		execute, queryType = transaction.ExecContext, transaction.QueryRowContext
	}
	output.Dest = contract.OracleExplicitKeyDestination(data, primary)
	if output.Dest == nil {
		if transaction == nil {
			// CURRENT_SCHEMA 属于 Oracle 会话，查询类型和写入必须使用同一连接。
			pinned, err := c.DB.Conn(ctx)
			if err != nil {
				return InsertResult{}, err
			}
			defer func() { _ = pinned.Close() }()
			execute, queryType = pinned.ExecContext, pinned.QueryRowContext
		}
		var err error
		output.Dest, err = c.oracleCatalogKeyDestination(ctx, queryType, request.Table(), primary)
		if err != nil {
			return InsertResult{}, err
		}
	}
	values[last] = output
	if _, err := execute(ctx, c.Builder.Rebind(query), values...); err != nil {
		return InsertResult{}, err
	}
	result := InsertResult{Affected: 1, Data: data}
	id, err := sqlOutputValue(output.Dest)
	if err == nil {
		if text, isText := id.(string); isText && text == "" {
			err = ErrInsertIDUnavailable
		}
	}
	if err != nil {
		return result, &PartialWriteError{Result: result, Cause: err}
	}
	result.ID, result.IDKnown = id, true
	return result, result.Validate()
}

func (c *SQLConnection) oracleCatalogKeyDestination(ctx context.Context, queryRow oracleTypeQuery, table, primary string) (any, error) {
	namespace, name, err := splitSchemaTable(table)
	if err != nil {
		return nil, err
	}
	const catalog = `SELECT data_type FROM all_tab_columns
WHERE owner = NVL(?, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) AND table_name = ? AND column_name = ?`
	var columnType string
	if err := queryRow(ctx, c.Builder.Rebind(catalog), strings.ToUpper(namespace), strings.ToUpper(name), strings.ToUpper(primary)).Scan(&columnType); err != nil {
		return nil, fmt.Errorf("%w: 查询 Oracle 主键 %s.%s 类型失败: %w", ErrInsertIDUnavailable, table, primary, err)
	}
	switch strings.ToUpper(strings.TrimSpace(columnType)) {
	case "CHAR", "NCHAR", "VARCHAR", "VARCHAR2", "NVARCHAR2":
		return new(string), nil
	case "NUMBER", "FLOAT", "BINARY_FLOAT", "BINARY_DOUBLE":
		return new(int64), nil
	default:
		return nil, fmt.Errorf("%w: Oracle 不支持回传主键 %s.%s 的类型 %q", ErrInsertIDUnavailable, table, primary, columnType)
	}
}
