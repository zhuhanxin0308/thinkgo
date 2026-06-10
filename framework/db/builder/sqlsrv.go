package builder

import (
	"fmt"
	"strings"
)

// Sqlsrv builder（SQL Server 方言）
// 统一使用 ? 占位符构建，执行前由 Rebind 转换为 @pN 风格。
type Sqlsrv struct{}

func sqlsrvQuote(name string) string {
	return quoteWith(name, "[", "]")
}

func sqlsrvQuoteFields(fields string) string {
	return quoteFieldsWith(fields, "[", "]")
}

// Rebind 将 ? 占位符转换为 SQL Server 的 @p1、@p2... 风格。
func (s *Sqlsrv) Rebind(query string) string {
	return rebindNumbered(query, "@p")
}

// Select builds a SELECT query
func (s *Sqlsrv) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	// SQL Server 2012+ support OFFSET FETCH
	query := fmt.Sprintf("SELECT %s FROM %s", sqlsrvQuoteFields(fields), sqlsrvQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if order == "" {
		order = "(SELECT NULL)" // Required for OFFSET FETCH if no order
	}
	query += " ORDER BY " + order

	if limit > 0 {
		query += fmt.Sprintf(" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	}
	return query
}

// Insert builds an INSERT query
func (s *Sqlsrv) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for k, v := range data {
		keys = append(keys, sqlsrvQuote(k))
		values = append(values, v)
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s); SELECT SCOPE_IDENTITY()",
		sqlsrvQuote(table),
		strings.Join(keys, ", "),
		strings.Join(placeholders, ", "))

	return query, values
}

// Update builds an UPDATE query
func (s *Sqlsrv) Update(table string, data map[string]interface{}, where []string) (string, []interface{}) {
	sets := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	for k, v := range data {
		sets = append(sets, fmt.Sprintf("%s = ?", sqlsrvQuote(k)))
		values = append(values, v)
	}

	query := fmt.Sprintf("UPDATE %s SET %s", sqlsrvQuote(table), strings.Join(sets, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}

	return query, values
}

// Delete builds a DELETE query
func (s *Sqlsrv) Delete(table string, where []string) string {
	query := fmt.Sprintf("DELETE FROM %s", sqlsrvQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}

// Count builds a COUNT query
func (s *Sqlsrv) Count(table string, where []string) string {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", sqlsrvQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}
