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

// QuoteIdentifier 用方括号引用标识符。
func (s *Sqlsrv) QuoteIdentifier(name string) string {
	return sqlsrvQuote(name)
}

// Select builds a SELECT query
func (s *Sqlsrv) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	query := fmt.Sprintf("SELECT %s FROM %s", sqlsrvQuoteFields(fields), sqlsrvQuote(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	orderClause, limitClause := s.Pagination(order, limit, offset)
	return query + orderClause + limitClause
}

// Pagination SQL Server 2012+ 使用 OFFSET..FETCH，且分页必须存在 ORDER BY。
func (s *Sqlsrv) Pagination(order string, limit int, offset int) (string, string) {
	paging := limit > 0 || offset > 0
	orderClause := ""
	if order != "" {
		orderClause = " ORDER BY " + order
	} else if paging {
		// OFFSET..FETCH 要求 ORDER BY，无显式排序时补一个确定性的占位排序。
		orderClause = " ORDER BY (SELECT NULL)"
	}

	limitClause := ""
	if paging {
		limitClause = fmt.Sprintf(" OFFSET %d ROWS", offset)
		if limit > 0 {
			limitClause += fmt.Sprintf(" FETCH NEXT %d ROWS ONLY", limit)
		}
	}
	return orderClause, limitClause
}

// LockClause SQL Server 用表提示实现行锁，简单查询难以安全表达，这里不输出尾子句。
func (s *Sqlsrv) LockClause(string) string { return "" }

// SupportsLastInsertId SQL Server 驱动不可靠支持 LastInsertId，需走 OUTPUT INSERTED。
func (s *Sqlsrv) SupportsLastInsertId() bool { return false }

// InsertReturning 构建 INSERT ... OUTPUT INSERTED.<pk> 语句，让连接层可用 QueryRow 扫描主键。
func (s *Sqlsrv) InsertReturning(table string, data map[string]interface{}, primaryKey string) (string, []interface{}, bool) {
	if primaryKey == "" {
		primaryKey = "id"
	}
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for k, v := range data {
		keys = append(keys, sqlsrvQuote(k))
		values = append(values, v)
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) OUTPUT INSERTED.%s VALUES (%s)",
		sqlsrvQuote(table),
		strings.Join(keys, ", "),
		sqlsrvQuote(primaryKey),
		strings.Join(placeholders, ", "))

	return query, values, true
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

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
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
