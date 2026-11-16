package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/db/internal/contract"
)

type oracleReturningSession interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

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
	var session oracleReturningSession
	if transaction != nil {
		session = transaction
		execute = transaction.ExecContext
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
			session = pinned
			execute = pinned.ExecContext
		}
		var err error
		output.Dest, err = c.oracleCatalogKeyDestination(ctx, session, request.Table(), primary)
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

func (c *SQLConnection) oracleCatalogKeyDestination(ctx context.Context, session oracleReturningSession, table, primary string) (any, error) {
	namespace, name, err := splitSchemaTable(table)
	if err != nil {
		return nil, err
	}
	const catalog = `SELECT data_type FROM all_tab_columns
WHERE owner = NVL(?, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) AND table_name = ? AND column_name = ?`
	query := c.Builder.Rebind(catalog)
	column := strings.ToUpper(primary)
	var columnType string
	err = session.QueryRowContext(ctx, query, strings.ToUpper(namespace), strings.ToUpper(name), column).Scan(&columnType)
	if errors.Is(err, sql.ErrNoRows) {
		// Oracle 负责按当前模式解析私有与公共同义词及其引用链。
		namespace, name, err = c.resolveOracleReturningTable(ctx, session, table)
		if err == nil {
			err = session.QueryRowContext(ctx, query, namespace, name, column).Scan(&columnType)
		}
	}
	if err != nil {
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

func (c *SQLConnection) resolveOracleReturningTable(ctx context.Context, session oracleReturningSession, table string) (string, string, error) {
	// NAME_RESOLVE 的 context 0 代表表名解析，未使用的输出由 PL/SQL 局部变量接收。
	const resolve = `DECLARE
    unused_part VARCHAR2(128);
    unused_type NUMBER;
    unused_object NUMBER;
BEGIN
    SYS.DBMS_UTILITY.NAME_RESOLVE(?, 0, ?, ?, unused_part, ?, unused_type, unused_object);
END;`
	var owner, name, link string
	if _, err := session.ExecContext(ctx, c.Builder.Rebind(resolve), c.Builder.QuoteIdentifier(table),
		sql.Out{Dest: &owner}, sql.Out{Dest: &name}, sql.Out{Dest: &link}); err != nil {
		return "", "", err
	}
	if link != "" || owner == "" || name == "" {
		return "", "", errors.New("主键回传要求可解析的 Oracle 本地表")
	}
	return owner, name, nil
}
