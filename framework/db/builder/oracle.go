//go:build oracle
// +build oracle

package builder

import (
	"fmt"
	"strings"
)

// Oracle builder（Oracle 方言）
// 统一使用 ? 占位符构建，执行前由 Rebind 转换为 :N 风格。
type Oracle struct{}

func oracleQuote(name string) string {
	return quoteWith(name, `"`, `"`)
}

func oracleQuoteFields(fields string) string {
	return quoteFieldsWith(fields, `"`, `"`)
}

// Rebind 将 ? 占位符转换为 Oracle 的 :1、:2... 风格。
func (o *Oracle) Rebind(query string) string {
	return rebindNumbered(query, ":")
}

// QuoteIdentifier 用双引号引用标识符。
func (o *Oracle) QuoteIdentifier(name string) string {
	return oracleQuote(name)
}

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
		orderClause = " ORDER BY " + order
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

// LockClause Oracle 支持 FOR UPDATE。
func (o *Oracle) LockClause(mode string) string {
	switch mode {
	case "FOR UPDATE", "LOCK IN SHARE MODE":
		return " FOR UPDATE"
	default:
		return ""
	}
}

// SupportsLastInsertId Oracle 不支持 LastInsertId，需走 RETURNING INTO（此处保守标记，交由上层处理）。
func (o *Oracle) SupportsLastInsertId() bool { return false }

// InsertReturning Oracle 的 RETURNING INTO 需绑定输出参数，标准 database/sql 难以统一表达，返回不支持。
func (o *Oracle) InsertReturning(string, map[string]interface{}, string) (string, []interface{}, bool) {
	return "", nil, false
}

// Insert builds an INSERT query
func (o *Oracle) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for k, v := range data {
		keys = append(keys, oracleQuote(k))
		values = append(values, v)
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		oracleQuote(table),
		strings.Join(keys, ", "),
		strings.Join(placeholders, ", "))

	return query, values
}

// Update builds an UPDATE query
func (o *Oracle) Update(table string, data map[string]interface{}, where []string) (string, []interface{}) {
	sets := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	for k, v := range data {
		sets = append(sets, fmt.Sprintf("%s = ?", oracleQuote(k)))
		values = append(values, v)
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
