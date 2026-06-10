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

// Select 构建 SELECT 查询
func (m *Mysql) Select(table string, fields string, where []string, order string, limit int, offset int) string {
	query := fmt.Sprintf("SELECT %s FROM %s", quoteFields(fields), quoteIdentifier(table))
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

// Insert 构建 INSERT 查询
func (m *Mysql) Insert(table string, data map[string]interface{}) (string, []interface{}) {
	keys := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))
	placeholders := make([]string, 0, len(data))

	for k, v := range data {
		keys = append(keys, quoteIdentifier(k))
		values = append(values, v)
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

	for k, v := range data {
		sets = append(sets, fmt.Sprintf("%s = ?", quoteIdentifier(k)))
		values = append(values, v)
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
