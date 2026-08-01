package db

import (
	"fmt"
	"strings"
)

var safeClauseKeywords = map[string]bool{
	"and": true, "or": true, "between": true, "is": true, "not": true,
	"null": true, "like": true, "in": true, "true": true, "false": true,
	"current_timestamp": true, "current_date": true, "current_time": true,
}

// resolveTable 结合配置的前缀解析出最终的表名。
func (q *Query) resolveTable() string {
	if q.rawTableName {
		return q.table
	}
	if q.db == nil {
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
	if q == nil || q.db == nil {
		return nil
	}
	if q.txOwner != nil && q.txOwner.connection != nil {
		return q.txOwner.connection.Builder
	}
	q.db.mu.RLock()
	defer q.db.mu.RUnlock()
	if q.db.connection == nil {
		return nil
	}
	if sqlConn, ok := q.db.connection.connection.(*SQLConnection); ok {
		if sqlConn == nil {
			return nil
		}
		return sqlConn.Builder
	}
	return nil
}

func (q *Query) transactionConnection() (*SQLConnection, error) {
	if q == nil || q.txOwner == nil || q.txOwner.connection == nil {
		return nil, fmt.Errorf("%w: 查询未绑定有效事务连接", ErrInvalidTransaction)
	}
	if err := q.txOwner.active(); err != nil {
		return nil, err
	}
	return q.txOwner.connection, nil
}

// quoteSafeClause 引用已通过 Query 安全入口校验的字段 token；Raw 入口不会调用本函数。
func (q *Query) quoteSafeClause(clause string) string {
	builder := q.builder()
	if builder == nil || clause == "" {
		return clause
	}
	locations := expressionWordPattern.FindAllStringIndex(clause, -1)
	if len(locations) == 0 {
		return clause
	}
	var result strings.Builder
	last := 0
	for _, location := range locations {
		result.WriteString(clause[last:location[0]])
		token := clause[location[0]:location[1]]
		lower := strings.ToLower(token)
		isFunction := false
		for index := location[1]; index < len(clause); index++ {
			if clause[index] == ' ' || clause[index] == '\t' || clause[index] == '\n' || clause[index] == '\r' {
				continue
			}
			isFunction = clause[index] == '('
			break
		}
		if safeClauseKeywords[lower] || isFunction && allowedExpressionFunctions[lower] {
			result.WriteString(token)
		} else {
			result.WriteString(builder.QuoteIdentifier(token))
		}
		last = location[1]
	}
	result.WriteString(clause[last:])
	return result.String()
}

// ensureValid 检查查询构建器状态是否合法。
func (q *Query) ensureValid() error {
	if q == nil {
		return fmt.Errorf("%w: 查询器不能为空", ErrInvalidQuery)
	}
	if q.err != nil {
		return q.err
	}
	if q.table == "" {
		return fmt.Errorf("%w: 表名不能为空", ErrInvalidQuery)
	}
	if q.db == nil {
		return ErrDatabaseUnavailable
	}
	if q.txOwner != nil {
		if err := q.txOwner.active(); err != nil {
			return err
		}
	} else if err := q.db.WithConnection(func(Connection) error { return nil }); err != nil {
		return err
	}
	if err := q.validateQueryArgumentAppend(0); err != nil {
		return err
	}
	return nil
}

// logContext 返回查询构建器上下文，便于调试。
// 注意：where 片段为参数化 SQL（如 "id = ?"），不含值；绑定参数仅记录数量，
// 不记录具体值，避免敏感数据（密码/令牌/PII）通过错误日志泄露。
func (q *Query) logContext() map[string]interface{} {
	dialect := ""
	if currentBuilder := q.builder(); currentBuilder != nil {
		dialect = currentBuilder.DialectName()
	}
	where := make([]string, 0, len(q.where))
	for _, condition := range q.where {
		where = append(where, redactSQLText(condition, dialect))
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
	if err == nil {
		return nil
	}
	if q == nil || q.db == nil {
		return err
	}
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
	fields := q.fields
	if q.aggregateExpression != nil {
		if err := q.aggregateExpression.validate(); err != nil {
			return "", nil, q.reportError("build_sql", err, nil)
		}
		fields = fmt.Sprintf("%s(%s) AS %s", strings.ToUpper(strings.TrimSpace(q.aggregateExpression.Function)), q.aggregateExpression.Field, q.aggregateExpression.Alias)
	}
	group := q.group
	// JOIN 默认只返回主表列，避免不同表的同名列在 map 结果中静默覆盖。
	// 关联表字段必须通过 Field 显式选择并使用唯一别名。
	if fields == "*" && len(q.joins) > 0 {
		fields = tableName + ".*"
	}
	builder := q.builder()
	lockSpec := LockSpec{}
	if builder != nil {
		tableName = builder.QuoteIdentifier(tableName)
		fields = builder.QuoteFields(fields)
		if group != "" {
			group = builder.QuoteFields(group)
		}
		var err error
		lockSpec, err = builder.Lock(q.lockMode)
		if err != nil {
			return "", nil, q.reportError("build_sql", err, nil)
		}
		if builder.DialectName() == "oracle" && q.lockMode != LockNone && (q.limit > 0 || q.offset > 0) {
			return "", nil, q.reportError("build_sql", fmt.Errorf("%w: Oracle 不支持分页与悲观锁组合", ErrUnsupportedFeature), nil)
		}
	} else {
		switch q.lockMode {
		case LockNone:
		case LockForUpdate:
			lockSpec.Tail = " FOR UPDATE"
		case LockForShare:
			lockSpec.Tail = " LOCK IN SHARE MODE"
		default:
			return "", nil, q.reportError("build_sql", ErrUnsupportedLockMode, nil)
		}
	}
	var sqlBuilder strings.Builder
	allArgs := make([]interface{}, 0, len(q.args)+len(q.havingArgs))

	sqlBuilder.WriteString("SELECT ")
	if q.distinct {
		sqlBuilder.WriteString("DISTINCT ")
	}
	sqlBuilder.WriteString(fields)

	sqlBuilder.WriteString(" FROM ")
	sqlBuilder.WriteString(tableName)
	sqlBuilder.WriteString(lockSpec.TableHint)

	for _, join := range q.joins {
		joinTable := q.resolveJoinTable(join)
		if builder != nil {
			joinTable = builder.QuoteIdentifier(joinTable)
		}
		sqlBuilder.WriteString(fmt.Sprintf(" %s %s ON %s", join.joinType, joinTable, join.condition))
	}

	if len(q.where) > 0 {
		sqlBuilder.WriteString(" WHERE ")
		sqlBuilder.WriteString(strings.Join(q.where, " AND "))
		allArgs = append(allArgs, q.args...)
	}

	if group != "" {
		sqlBuilder.WriteString(" GROUP BY ")
		sqlBuilder.WriteString(group)
	}

	if q.having != "" {
		sqlBuilder.WriteString(" HAVING ")
		sqlBuilder.WriteString(q.having)
		allArgs = append(allArgs, q.havingArgs...)
	}

	// 排序 + 分页：交由方言构建器生成，避免硬编码 MySQL 的 LIMIT/OFFSET
	// 在 SQL Server/Oracle 上产生非法 SQL。无方言构建器（Mock 连接）时回退 MySQL 风格。
	if builder != nil {
		orderClause, limitClause := builder.Pagination(q.order, q.limit, q.offset)
		sqlBuilder.WriteString(orderClause)
		sqlBuilder.WriteString(limitClause)
		sqlBuilder.WriteString(lockSpec.Tail)
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
		sqlBuilder.WriteString(lockSpec.Tail)
	}

	return sqlBuilder.String(), allArgs, nil
}

// needsRawSelect 判断查询是否必须走 RawQueryable 完整 SQL 通道
// （JOIN/GROUP/HAVING/DISTINCT/悲观锁均无法通过简单 Builder.Select 表达）。
func (q *Query) needsRawSelect() bool {
	return len(q.joins) > 0 || q.group != "" || q.having != "" || q.distinct || q.lockMode != LockNone
}

// clone 克隆一个全新的 Query 实例，用于保证链式调用状态完全隔离和安全。
func (q *Query) clone() *Query {
	return q.cloneWithCapacity(0, 0)
}

// cloneWithCapacity 为批量条件派生查询预留 where 与参数容量。
func (q *Query) cloneWithCapacity(whereExtra, argsExtra int) *Query {
	if q == nil {
		return nil
	}
	cloned := *q
	if q.where == nil && whereExtra <= 0 {
		cloned.where = nil
	} else {
		whereCapacity := len(q.where)
		if whereExtra > 0 {
			whereCapacity += whereExtra
			if whereCapacity < len(q.where) {
				whereCapacity = len(q.where)
			}
		}
		cloned.where = make([]string, len(q.where), whereCapacity)
		copy(cloned.where, q.where)
	}
	cloned.args = cloneDatabaseValuesWithExtraCapacity(q.args, argsExtra)
	cloned.joins = append([]joinClause(nil), q.joins...)
	cloned.havingArgs = cloneDatabaseValues(q.havingArgs)
	cloned.setExprs = append([]setExpression(nil), q.setExprs...)
	if q.aggregateExpression != nil {
		aggregate := *q.aggregateExpression
		cloned.aggregateExpression = &aggregate
	}
	return &cloned
}

func (q *Query) operationPredicate() (Predicate, error) {
	if q == nil || len(q.where) == 0 {
		return newPredicate(), nil
	}
	if len(q.where) == 1 {
		// 单个条件片段的参数全部属于该片段，避免重复扫描 SQL 统计占位符。
		predicate := newPredicate().appendWithConnector("AND", q.where[0], q.args, q.hasRawPredicate)
		return predicate, nil
	}
	predicate := newPredicate()
	argumentOffset := 0
	for _, clause := range q.where {
		count, err := countSQLPlaceholders(clause)
		if err != nil {
			return Predicate{}, err
		}
		if count < 0 || argumentOffset+count > len(q.args) {
			return Predicate{}, fmt.Errorf("%w: 条件参数数量不匹配", ErrInvalidQuery)
		}
		predicate = predicate.appendWithConnector("AND", clause, q.args[argumentOffset:argumentOffset+count], q.hasRawPredicate)
		argumentOffset += count
	}
	if argumentOffset != len(q.args) {
		return Predicate{}, fmt.Errorf("%w: 条件参数数量不匹配", ErrInvalidQuery)
	}
	return predicate, nil
}
