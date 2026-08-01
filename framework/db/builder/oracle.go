//go:build oracle
// +build oracle

package builder

import (
	"database/sql"
	"fmt"
	"strings"

	"thinkgo/framework/db/internal/contract"
)

// Oracle builder（Oracle 方言）
// 统一使用 ? 占位符构建，执行前由 Rebind 转换为 :N 风格。
type Oracle struct{}

func (o *Oracle) DialectName() string { return "oracle" }

func oracleQuote(name string) string {
	return quoteWith(strings.ToUpper(strings.TrimSpace(name)), `"`, `"`)
}

func oracleQuoteFields(fields string) string {
	return quoteFieldsWith(strings.ToUpper(fields), `"`, `"`)
}

// Rebind 将 ? 占位符转换为 Oracle 的 :1、:2... 风格。
func (o *Oracle) Rebind(query string) string {
	return rebindNumbered(query, ":")
}

// QuoteIdentifier 用双引号引用标识符。
func (o *Oracle) QuoteIdentifier(name string) string {
	return oracleQuote(name)
}

func (o *Oracle) QuoteFields(fields string) string { return oracleQuoteFields(fields) }

// Select builds a SELECT query
func (o *Oracle) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	query := fmt.Sprintf("SELECT %s FROM %s", oracleQuoteFields(fields), oracleQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	orderClause, limitClause := o.Pagination(order, limit, offset)
	return query + orderClause + limitClause
}

// Pagination Oracle 12c+ 使用 OFFSET..FETCH。
func (o *Oracle) Pagination(order string, limit int, offset int) (string, string) {
	orderClause := ""
	if order != "" {
		orderClause = " ORDER BY " + quoteOrderWith(strings.ToUpper(order), `"`, `"`)
	}
	limitClause := ""
	if limit > 0 || offset > 0 {
		limitClause = fmt.Sprintf(" OFFSET %d ROWS", offset)
		if limit > 0 {
			limitClause += fmt.Sprintf(" FETCH NEXT %d ROWS ONLY", limit)
		}
	}
	return orderClause, limitClause
}

func (o *Oracle) Lock(mode contract.LockMode) (contract.LockSpec, error) {
	switch mode {
	case contract.LockNone:
		return contract.LockSpec{}, nil
	case contract.LockForUpdate:
		return contract.LockSpec{Tail: " FOR UPDATE"}, nil
	case contract.LockForShare:
		return contract.LockSpec{}, contract.ErrUnsupportedFeature
	default:
		return contract.LockSpec{}, contract.ErrUnsupportedLockMode
	}
}

// LockClause 保留旧版字符串锁子句兼容入口。
func (o *Oracle) LockClause(mode string) string {
	if mode == "FOR UPDATE" || mode == "LOCK IN SHARE MODE" {
		return " FOR UPDATE"
	}
	return ""
}

// SupportsLastInsertId Oracle 不支持 LastInsertId，需走 RETURNING INTO（此处保守标记，交由上层处理）。
func (o *Oracle) SupportsLastInsertId() bool { return false }

// MaxBindParams 返回 Oracle INSERT ALL 的保守目标列预算；
// 12c/19c 的多表插入全部 INTO 合计不得超过 999 个目标列。
func (o *Oracle) MaxBindParams() int { return 999 }

// InsertReturning 使用 sql.Out 绑定 RETURNING INTO 的整数主键。
func (o *Oracle) InsertReturning(table string, data map[string]interface{}, primaryKey string) (string, []interface{}, bool) {
	if primaryKey == "" {
		primaryKey = "id"
	}
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data)+1)
	placeholders := make([]string, 0, len(data))
	for _, key := range sortedMapKeys(data) {
		keys = append(keys, oracleQuote(key))
		values = append(values, data[key])
		placeholders = append(placeholders, "?")
	}
	identifier := new(int64)
	values = append(values, sql.Out{Dest: identifier})
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s INTO ?",
		oracleQuote(table), strings.Join(keys, ", "), strings.Join(placeholders, ", "), oracleQuote(primaryKey))
	return query, values, true
}

// Insert builds an INSERT query
func (o *Oracle) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for _, k := range sortedMapKeys(data) {
		keys = append(keys, oracleQuote(k))
		values = append(values, data[k])
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		oracleQuote(table),
		strings.Join(keys, ", "),
		strings.Join(placeholders, ", "))

	return query, values
}

// InsertBatch 使用 Oracle 12c+ 支持的 INSERT ALL 生成单表多行写入，
// 避免输出 Oracle 不支持的逗号分隔多行 VALUES 语法。
func (o *Oracle) InsertBatch(table string, fields []string, rows []map[string]interface{}) (string, []interface{}) {
	quotedFields := make([]string, len(fields))
	placeholders := make([]string, len(fields))
	for index, field := range fields {
		quotedFields[index] = oracleQuote(field)
		placeholders[index] = "?"
	}

	quotedTable := oracleQuote(table)
	fieldList := strings.Join(quotedFields, ", ")
	valueList := strings.Join(placeholders, ", ")
	values := make([]interface{}, 0, len(rows)*len(fields))
	var query strings.Builder
	query.WriteString("INSERT ALL")
	for _, row := range rows {
		query.WriteString(" INTO ")
		query.WriteString(quotedTable)
		query.WriteString(" (")
		query.WriteString(fieldList)
		query.WriteString(") VALUES (")
		query.WriteString(valueList)
		query.WriteString(")")
		for _, field := range fields {
			values = append(values, row[field])
		}
	}
	query.WriteString(" SELECT 1 FROM DUAL")
	return query.String(), values
}

// Update builds an UPDATE query
func (o *Oracle) Update(table string, data map[string]interface{}, where []string) (string, []interface{}) {
	sets := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	for _, k := range sortedMapKeys(data) {
		sets = append(sets, fmt.Sprintf("%s = ?", oracleQuote(k)))
		values = append(values, data[k])
	}

	query := fmt.Sprintf("UPDATE %s SET %s", oracleQuote(table), strings.Join(sets, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}

	return query, values
}

// Delete builds a DELETE query
func (o *Oracle) Delete(table string, where []string) string {
	query := fmt.Sprintf("DELETE FROM %s", oracleQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}

// Count builds a COUNT query
func (o *Oracle) Count(table string, where []string) string {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", oracleQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}
