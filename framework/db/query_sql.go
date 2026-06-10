package db

import (
	"fmt"
	"strings"
)

// resolveTable 结合配置的前缀解析出最终的表名。
func (q *Query) resolveTable() string {
	if q.rawTableName {
		return q.table
	}
	return q.db.prefix + q.table
}

// rebind 把统一的 ? 占位符 SQL 转换为底层连接对应方言的占位符风格。
// 用于事务执行器和批量写入等绕过 SQLConnection 的直连路径，保证占位符一致。
func (q *Query) rebind(sqlStr string) string {
	if sqlConn, ok := q.db.connection.(*SQLConnection); ok && sqlConn.Builder != nil {
		return sqlConn.Builder.Rebind(sqlStr)
	}
	return sqlStr
}

// ensureValid 检查查询构建器状态是否合法。
func (q *Query) ensureValid() error {
	if q.err != nil {
		return q.err
	}
	if q.table == "" {
		return fmt.Errorf("table name cannot be empty")
	}
	return nil
}

// logContext 返回查询构建器上下文，便于调试。
func (q *Query) logContext() map[string]interface{} {
	return map[string]interface{}{
		"table":  q.table,
		"where":  append([]string(nil), q.where...),
		"args":   append([]interface{}(nil), q.args...),
		"order":  q.order,
		"limit":  q.limit,
		"offset": q.offset,
	}
}

// reportError 上报查询错误。
func (q *Query) reportError(operation string, err error, extra map[string]interface{}) error {
	ctx := q.logContext()
	for key, value := range extra {
		ctx[key] = value
	}
	return q.db.reportError(operation, err, ctx)
}

// BuildSelectSQL 构造完整的 SELECT SQL 语句（不执行），通常用于子查询或调试。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->buildSql()
func (q *Query) BuildSelectSQL() (string, []interface{}, error) {
	if err := q.ensureValid(); err != nil {
		return "", nil, q.reportError("build_sql", err, nil)
	}

	tableName := q.resolveTable()
	var sqlBuilder strings.Builder
	allArgs := make([]interface{}, 0, len(q.args)+len(q.havingArgs))

	sqlBuilder.WriteString("SELECT ")
	if q.distinct {
		sqlBuilder.WriteString("DISTINCT ")
	}
	sqlBuilder.WriteString(q.fields)

	sqlBuilder.WriteString(" FROM ")
	sqlBuilder.WriteString(tableName)

	for _, join := range q.joins {
		sqlBuilder.WriteString(fmt.Sprintf(" %s %s ON %s", join.joinType, join.table, join.condition))
	}

	if len(q.where) > 0 {
		sqlBuilder.WriteString(" WHERE ")
		sqlBuilder.WriteString(strings.Join(q.where, " AND "))
		allArgs = append(allArgs, q.args...)
	}

	if q.group != "" {
		sqlBuilder.WriteString(" GROUP BY ")
		sqlBuilder.WriteString(q.group)
	}

	if q.having != "" {
		sqlBuilder.WriteString(" HAVING ")
		sqlBuilder.WriteString(q.having)
		allArgs = append(allArgs, q.havingArgs...)
	}

	if q.order != "" {
		sqlBuilder.WriteString(" ORDER BY ")
		sqlBuilder.WriteString(q.order)
	}

	if q.limit > 0 {
		sqlBuilder.WriteString(fmt.Sprintf(" LIMIT %d", q.limit))
	}
	if q.offset > 0 {
		sqlBuilder.WriteString(fmt.Sprintf(" OFFSET %d", q.offset))
	}

	if q.lockMode != "" {
		sqlBuilder.WriteString(" ")
		sqlBuilder.WriteString(q.lockMode)
	}

	return sqlBuilder.String(), allArgs, nil
}

// needsRawSelect 判断查询是否必须走 RawQueryable 完整 SQL 通道
// （JOIN/GROUP/HAVING/DISTINCT/悲观锁均无法通过简单 Builder.Select 表达）。
func (q *Query) needsRawSelect() bool {
	return len(q.joins) > 0 || q.group != "" || q.having != "" || q.distinct || q.lockMode != ""
}

// clone 克隆一个全新的 Query 实例，用于保证链式调用状态完全隔离和安全。
func (q *Query) clone() *Query {
	cloned := *q
	cloned.where = append([]string(nil), q.where...)
	cloned.args = append([]interface{}(nil), q.args...)
	cloned.joins = append([]joinClause(nil), q.joins...)
	cloned.havingArgs = append([]interface{}(nil), q.havingArgs...)
	cloned.setExprs = append([]string(nil), q.setExprs...)
	return &cloned
}
