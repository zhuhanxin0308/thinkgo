package builder

import (
	"fmt"
	"strings"
)

// Sqlite builder（SQLite 方言，使用 ? 占位符与双引号标识符）
type Sqlite struct{}

func sqliteQuote(name string) string {
	return quoteWith(name, `"`, `"`)
}

func sqliteQuoteFields(fields string) string {
	return quoteFieldsWith(fields, `"`, `"`)
}

// Rebind SQLite 原生使用 ? 占位符，直接返回。
func (s *Sqlite) Rebind(query string) string {
	return query
}

// Select builds a SELECT query
func (s *Sqlite) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	query := fmt.Sprintf("SELECT %s FROM %s", sqliteQuoteFields(fields), sqliteQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if order != "" {
		query += " ORDER BY " + order
	}
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", offset)
	}
	return query
}

// Insert builds an INSERT query
func (s *Sqlite) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for k, v := range data {
		keys = append(keys, sqliteQuote(k))
		values = append(values, v)
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		sqliteQuote(table),
		strings.Join(keys, ", "),
		strings.Join(placeholders, ", "))

	return query, values
}

// Update builds an UPDATE query
func (s *Sqlite) Update(table string, data map[string]interface{}, where []string) (string, []interface{}) {
	sets := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	for k, v := range data {
		sets = append(sets, fmt.Sprintf("%s = ?", sqliteQuote(k)))
		values = append(values, v)
	}

	query := fmt.Sprintf("UPDATE %s SET %s", sqliteQuote(table), strings.Join(sets, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}

	return query, values
}

// Delete builds a DELETE query
func (s *Sqlite) Delete(table string, where []string) string {
	query := fmt.Sprintf("DELETE FROM %s", sqliteQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}

// Count builds a COUNT query
func (s *Sqlite) Count(table string, where []string) string {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", sqliteQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}
