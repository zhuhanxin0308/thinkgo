package builder

import (
	"fmt"
	"strings"
)

// Mysql SQL 构建器（MySQL 方言）
// 对应 ThinkPHP 8 的 think\db\builder\Mysql
type Mysql struct{}

// quoteIdentifier 用反引号包裹标识符，防止与 SQL 关键字冲突及注入风险
func quoteIdentifier(name string) string {
	return quoteWith(name, "`", "`")
}

// quoteFields 处理字段列表（逗号分隔）
func quoteFields(fields string) string {
	return quoteFieldsWith(fields, "`", "`")
}

// Rebind MySQL 原生即使用 ? 占位符，直接返回。
func (m *Mysql) Rebind(query string) string {
	return query
}

// QuoteIdentifier 用反引号引用标识符。
func (m *Mysql) QuoteIdentifier(name string) string {
	return quoteIdentifier(name)
}

func (m *Mysql) QuoteFields(fields string) string { return quoteFields(fields) }

// Select 构建 SELECT 查询
func (m *Mysql) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	query := fmt.Sprintf("SELECT %s FROM %s", quoteFields(fields), quoteIdentifier(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	orderClause, limitClause := m.Pagination(order, limit, offset)
	return query + orderClause + limitClause
}

// Pagination MySQL 使用 LIMIT/OFFSET 语法。
func (m *Mysql) Pagination(order string, limit int, offset int) (string, string) {
	orderClause := ""
	if order != "" {
		orderClause = " ORDER BY " + quoteOrderWith(order, "`", "`")
	}
	limitClause := ""
	if limit > 0 {
		limitClause += fmt.Sprintf(" LIMIT %d", limit)
	} else if offset > 0 {
		limitClause = " LIMIT 18446744073709551615"
	}
	if offset > 0 {
		limitClause += fmt.Sprintf(" OFFSET %d", offset)
	}
	return orderClause, limitClause
}

// LockClause MySQL 直接使用 FOR UPDATE / LOCK IN SHARE MODE。
func (m *Mysql) LockClause(mode string) string {
	if mode == "" {
		return ""
	}
	return " " + mode
}

// SupportsLastInsertId MySQL 支持 LastInsertId。
func (m *Mysql) SupportsLastInsertId() bool { return true }

// MaxBindParams 返回 MySQL 单语句占位符上限。
func (m *Mysql) MaxBindParams() int { return 65535 }

// InsertReturning MySQL 无需 RETURNING 写法。
func (m *Mysql) InsertReturning(string, map[string]interface{}, string) (string, []interface{}, bool) {
	return "", nil, false
}

// Insert 构建 INSERT 查询
func (m *Mysql) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for _, k := range sortedMapKeys(data) {
		keys = append(keys, quoteIdentifier(k))
		values = append(values, data[k])
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteIdentifier(table),
		strings.Join(keys, ", "),
		strings.Join(placeholders, ", "))

	return query, values
}

// Update 构建 UPDATE 查询
func (m *Mysql) Update(table string, data map[string]interface{}, where []string) (string, []interface{}) {
	sets := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	for _, k := range sortedMapKeys(data) {
		sets = append(sets, fmt.Sprintf("%s = ?", quoteIdentifier(k)))
		values = append(values, data[k])
	}

	query := fmt.Sprintf("UPDATE %s SET %s", quoteIdentifier(table), strings.Join(sets, ", "))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}

	return query, values
}

// Delete 构建 DELETE 查询
func (m *Mysql) Delete(table string, where []string) string {
	query := fmt.Sprintf("DELETE FROM %s", quoteIdentifier(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}

// Count 构建 COUNT 查询
func (m *Mysql) Count(table string, where []string) string {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdentifier(table))
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return query
}
