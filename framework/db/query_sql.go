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

// resolveJoinTable 为 JOIN 表套用与主表一致的前缀规则，避免主表带前缀而 JOIN 表不带导致表名错配。
// rawTable 为 true（通过 JoinRaw 注册）时按完整表名处理，不再追加前缀。
func (q *Query) resolveJoinTable(join joinClause) string {
	if join.rawTable || q.db == nil || q.db.prefix == "" {
		return join.table
	}
	return q.db.prefix + join.table
}

// rebind 把统一的 ? 占位符 SQL 转换为底层连接对应方言的占位符风格。
// 用于事务执行器和批量写入等绕过 SQLConnection 的直连路径，保证占位符一致。
func (q *Query) rebind(sqlStr string) string {
	if b := q.builder(); b != nil {
		return b.Rebind(sqlStr)
	}
	return sqlStr
}

// builder 返回底层 SQL 连接的方言构建器；非 SQL 连接（如 Mock/内存）返回 nil。
func (q *Query) builder() Builder {
	if sqlConn, ok := q.db.connection.(*SQLConnection); ok {
		return sqlConn.Builder
	}
	return nil
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
// 注意：where 片段为参数化 SQL（如 "id = ?"），不含值；绑定参数仅记录数量，
// 不记录具体值，避免敏感数据（密码/令牌/PII）通过错误日志泄露。
func (q *Query) logContext() map[string]interface{} {
	where := make([]string, 0, len(q.where))
	for _, condition := range q.where {
		where = append(where, redactSQLText(condition))
	}
	return map[string]interface{}{
		"table":     q.table,
		"where":     where,
		"arg_count": len(q.args),
		"order":     q.order,
		"limit":     q.limit,
		"offset":    q.offset,
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
		sqlBuilder.WriteString(fmt.Sprintf(" %s %s ON %s", join.joinType, q.resolveJoinTable(join), join.condition))
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

	// 排序 + 分页：交由方言构建器生成，避免硬编码 MySQL 的 LIMIT/OFFSET
	// 在 SQL Server/Oracle 上产生非法 SQL。无方言构建器（Mock 连接）时回退 MySQL 风格。
	if b := q.builder(); b != nil {
		orderClause, limitClause := b.Pagination(q.order, q.limit, q.offset)
		sqlBuilder.WriteString(orderClause)
		sqlBuilder.WriteString(limitClause)
		if q.lockMode != "" {
			sqlBuilder.WriteString(b.LockClause(q.lockMode))
		}
	} else {
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
