//go:build oracle
// +build oracle

package builder

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/db/internal/contract"
)

// Oracle builder（Oracle 方言）
// 统一使用 ? 占位符构建，执行前由 Rebind 转换为 :N 风格。
type Oracle struct{}

// Oracle 条件多表插入最多允许 127 个 WHEN 分支；每个源行恰好匹配一个分支。
const oracleMaximumBatchRows = 127

// MaxBatchRows 让通用批量执行器在条件分支上限前拆批，并为多批写入保留事务原子性。
func (o *Oracle) MaxBatchRows() int { return oracleMaximumBatchRows }

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

// MaxBindParams 保留历史批量绑定预算，限制单条语句大小及自动事务分批边界。
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

// InsertBatch 为每条输入记录生成独立源行，并且只执行与其序号对应的 INTO。
// 默认序列按源行递增，各列仍按目标表执行转换，兼容空值、大字段与混合绑定类型。
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
	for index, row := range rows {
		fmt.Fprintf(&query, " WHEN thinkgo_batch_row = %d THEN INTO ", index+1)
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
	fmt.Fprintf(&query, " SELECT LEVEL AS thinkgo_batch_row FROM DUAL CONNECT BY LEVEL <= %d", len(rows))
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
