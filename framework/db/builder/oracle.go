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

// Select builds a SELECT query
func (o *Oracle) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	query := fmt.Sprintf("SELECT %s FROM %s", oracleQuoteFields(fields), oracleQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if order != "" {
		query += " ORDER BY " + order
	}
	if limit > 0 {
		query += fmt.Sprintf(" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	}
	return query
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
