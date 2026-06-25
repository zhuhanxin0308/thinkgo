package builder

import (
	"fmt"
	"strings"
)

// Pgsql builder（PostgreSQL 方言）
// 统一使用 ? 占位符构建，执行前由 Rebind 转换为 $N 风格。
type Pgsql struct{}

func pgQuote(name string) string {
	return quoteWith(name, `"`, `"`)
}

func pgQuoteFields(fields string) string {
	return quoteFieldsWith(fields, `"`, `"`)
}

// Rebind 将 ? 占位符转换为 PostgreSQL 的 $1、$2... 风格。
func (p *Pgsql) Rebind(query string) string {
	return rebindNumbered(query, "$")
}

// QuoteIdentifier 用双引号引用标识符。
func (p *Pgsql) QuoteIdentifier(name string) string {
	return pgQuote(name)
}

// Select builds a SELECT query
func (p *Pgsql) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	query := fmt.Sprintf("SELECT %s FROM %s", pgQuoteFields(fields), pgQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	orderClause, limitClause := p.Pagination(order, limit, offset)
	return query + orderClause + limitClause
}

// Pagination PostgreSQL 使用 LIMIT/OFFSET 语法。
func (p *Pgsql) Pagination(order string, limit int, offset int) (string, string) {
	orderClause := ""
	if order != "" {
		orderClause = " ORDER BY " + order
	}
	limitClause := ""
	if limit > 0 {
		limitClause += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		limitClause += fmt.Sprintf(" OFFSET %d", offset)
	}
	return orderClause, limitClause
}

// LockClause PostgreSQL 用 FOR UPDATE / FOR SHARE。
func (p *Pgsql) LockClause(mode string) string {
	switch mode {
	case "FOR UPDATE":
		return " FOR UPDATE"
	case "LOCK IN SHARE MODE":
		return " FOR SHARE"
	case "":
		return ""
	default:
		return " " + mode
	}
}

// SupportsLastInsertId PostgreSQL（lib/pq）不支持 LastInsertId，需走 RETURNING。
func (p *Pgsql) SupportsLastInsertId() bool { return false }

// InsertReturning 构建 INSERT ... RETURNING 主键 的语句。
func (p *Pgsql) InsertReturning(table string, data map[string]interface{}, primaryKey string) (string, []interface{}, bool) {
	if primaryKey == "" {
		primaryKey = "id"
	}
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))
	for k, v := range data {
		keys = append(keys, pgQuote(k))
		values = append(values, v)
		placeholders = append(placeholders, "?")
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		pgQuote(table),
		strings.Join(keys, ", "),
		strings.Join(placeholders, ", "),
		pgQuote(primaryKey))
	return query, values, true
}

// Insert builds an INSERT query
func (p *Pgsql) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for k, v := range data {
		keys = append(keys, pgQuote(k))
		values = append(values, v)
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		pgQuote(table),
		strings.Join(keys, ", "),
		strings.Join(placeholders, ", "))

	return query, values
}

// Update builds an UPDATE query
func (p *Pgsql) Update(table string, data map[string]interface{}, where []string) (string, []interface{}) {
	sets := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	for k, v := range data {
		sets = append(sets, fmt.Sprintf("%s = ?", pgQuote(k)))
		values = append(values, v)
	}

	query := fmt.Sprintf("UPDATE %s SET %s", pgQuote(table), strings.Join(sets, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}

	return query, values
}

// Delete builds a DELETE query
func (p *Pgsql) Delete(table string, where []string) string {
	query := fmt.Sprintf("DELETE FROM %s", pgQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}

// Count builds a COUNT query
func (p *Pgsql) Count(table string, where []string) string {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", pgQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}
