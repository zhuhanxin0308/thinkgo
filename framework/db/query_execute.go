package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Find 查询单条记录。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->find()
// 在克隆上设置 LIMIT 1，避免污染调用方的查询构建器状态（终端方法应可重复执行）。
func (q *Query) Find() (map[string]interface{}, error) {
	cloned := q.clone()
	cloned.limit = 1
	rows, err := cloned.Select()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// Select 查询多条记录。
func (q *Query) Select() ([]map[string]interface{}, error) {
	if err := q.ensureValid(); err != nil {
		return nil, q.reportError("select", err, nil)
	}

	if q.txExecutor != nil {
		sqlCloned := q.clone()
		sqlStr, args, err := sqlCloned.BuildSelectSQL()
		if err != nil {
			return nil, err
		}
		return q.querySQLInTx(sqlStr, args...)
	}

	if q.needsRawSelect() {
		rows, err := q.selectAdvanced()
		if err != nil {
			return nil, q.reportError("select", err, nil)
		}
		return rows, nil
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return nil, q.reportError("select", stateErr, nil)
	}
	defer release()
	var rows []map[string]interface{}
	var err error
	table := q.resolveTable()
	if cc, ok := connection.(ContextualConnection); ok {
		rows, err = cc.SelectContext(q.context(), table, q.fields, q.where, q.args, q.order, q.limit, q.offset)
	} else {
		rows, err = connection.Select(table, q.fields, q.where, q.args, q.order, q.limit, q.offset)
	}
	if err != nil {
		return nil, q.reportError("select", err, map[string]interface{}{
			"fields": q.fields,
		})
	}
	return rows, nil
}

// selectAdvanced 构造带 JOIN / GROUP / HAVING / DISTINCT / 悲观锁的完整 SQL。
// 复用 BuildSelectSQL 作为唯一的 SQL 构建入口，避免与其重复维护两套拼接逻辑。
func (q *Query) selectAdvanced() ([]map[string]interface{}, error) {
	sqlStr, allArgs, err := q.BuildSelectSQL()
	if err != nil {
		return nil, err
	}
	return q.rawQuery(sqlStr, allArgs...)
}

// rawQuery 优先在支持 context 的连接上带上下文执行原生查询，否则回退到普通 RawQueryable。
func (q *Query) rawQuery(sqlStr string, args ...interface{}) ([]map[string]interface{}, error) {
	connection, release, err := q.db.acquireConnection()
	if err != nil {
		return nil, err
	}
	defer release()
	if cc, ok := connection.(ContextualRawQueryable); ok {
		return cc.QueryContext(q.context(), sqlStr, args...)
	}
	rawConn, ok := connection.(RawQueryable)
	if !ok {
		return nil, fmt.Errorf("当前数据库驱动不支持高级查询")
	}
	return rawConn.Query(sqlStr, args...)
}

// rawExecute 优先在支持 context 的连接上带上下文执行原生写操作，否则回退到普通 RawQueryable。
func (q *Query) rawExecute(sqlStr string, args ...interface{}) (int64, error) {
	connection, release, err := q.db.acquireConnection()
	if err != nil {
		return 0, err
	}
	defer release()
	if cc, ok := connection.(ContextualRawQueryable); ok {
		return cc.ExecuteContext(q.context(), sqlStr, args...)
	}
	rawConn, ok := connection.(RawQueryable)
	if !ok {
		return 0, fmt.Errorf("当前数据库驱动不支持原生执行")
	}
	return rawConn.Execute(sqlStr, args...)
}

// Insert 插入一条记录。
func (q *Query) Insert(data map[string]interface{}) (int64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("insert", err, nil)
	}
	if len(data) == 0 {
		return 0, q.reportError("insert", fmt.Errorf("插入数据不能为空"), nil)
	}
	if err := validateDataKeys(data); err != nil {
		wrappedErr := fmt.Errorf("unsafe insert fields: %w", err)
		return 0, q.reportError("insert", wrappedErr, redactDataKeys(data))
	}
	if len(q.setExprs) > 0 {
		return 0, q.reportError("insert", fmt.Errorf("%w: Insert 不支持 Inc/Dec 表达式", ErrInvalidQuery), nil)
	}
	workingData := cloneDatabaseMap(data)

	if q.autoTimestamp {
		now := time.Now()
		if err := setAutoTimestamp(workingData, q.createTimeField, now, q.timestampValueType); err != nil {
			return 0, q.reportError("insert", err, redactDataKeys(data))
		}
		if err := setAutoTimestamp(workingData, q.updateTimeField, now, q.timestampValueType); err != nil {
			return 0, q.reportError("insert", err, redactDataKeys(data))
		}
	}

	if q.txExecutor != nil {
		sqlConn, connectionErr := q.transactionConnection()
		if connectionErr != nil {
			return 0, q.reportError("insert", connectionErr, nil)
		}
		builder := sqlConn.Builder
		// 不支持 LastInsertId 的方言（如 PostgreSQL）在事务内同样改用 RETURNING 主键。
		if !builder.SupportsLastInsertId() {
			if query, values, ok := builder.InsertReturning(q.resolveTable(), workingData, q.insertPrimaryKey); ok {
				if len(values) > 0 {
					if output, isOutput := values[len(values)-1].(sql.Out); isOutput {
						if _, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), values...); err != nil {
							return 0, q.reportError("insert", err, redactDataKeys(workingData))
						}
						identifier, ok := output.Dest.(*int64)
						if !ok || identifier == nil {
							return 0, q.reportError("insert", fmt.Errorf("%w: RETURNING 输出目标必须是 *int64", ErrInvalidDatabaseRow), nil)
						}
						return *identifier, nil
					}
				}
				var id int64
				if err := q.txExecutor.QueryRowContext(q.context(), builder.Rebind(query), values...).Scan(&id); err != nil {
					return 0, q.reportError("insert", err, redactDataKeys(workingData))
				}
				return id, nil
			}
			return 0, q.reportError("insert", fmt.Errorf("%w: 当前方言不支持返回插入主键", ErrInvalidQuery), nil)
		}
		query, values := builder.Insert(q.resolveTable(), workingData)
		result, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), values...)
		if err != nil {
			return 0, q.reportError("insert", err, redactDataKeys(workingData))
		}
		return result.LastInsertId()
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return 0, q.reportError("insert", stateErr, nil)
	}
	defer release()
	var id int64
	var err error
	if sqlConnection, ok := connection.(*SQLConnection); ok && sqlConnection != nil {
		id, err = sqlConnection.InsertContextWithPrimaryKey(q.context(), q.resolveTable(), workingData, q.insertPrimaryKey)
	} else if cc, ok := connection.(ContextualConnection); ok {
		id, err = cc.InsertContext(q.context(), q.resolveTable(), workingData)
	} else {
		id, err = connection.Insert(q.resolveTable(), workingData)
	}
	if err != nil {
		return 0, q.reportError("insert", err, redactDataKeys(workingData))
	}
	return id, nil
}

// Update 更新记录。
// 安全检查：禁止无 WHERE 条件的 UPDATE，防止误更新全表数据。
func (q *Query) Update(data map[string]interface{}) (int64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("update", err, nil)
	}
	if len(q.where) == 0 {
		return 0, q.reportError("update", fmt.Errorf("%w: 表 %s 缺少 WHERE 条件", ErrUnsafeFullTableMutation, q.table), nil)
	}
	if len(data) == 0 && len(q.setExprs) == 0 {
		return 0, q.reportError("update", fmt.Errorf("更新数据不能为空"), nil)
	}
	if err := validateDataKeys(data); err != nil {
		wrappedErr := fmt.Errorf("unsafe update fields: %w", err)
		return 0, q.reportError("update", wrappedErr, redactDataKeys(data))
	}
	for _, expression := range q.setExprs {
		if _, duplicated := data[expression.field]; duplicated {
			return 0, q.reportError("update", fmt.Errorf("%w: 字段 %q 同时出现在 data 与 Inc/Dec 中", ErrInvalidQuery, expression.field), nil)
		}
	}
	workingData := cloneDatabaseMap(data)

	if q.autoTimestamp {
		if workingData == nil {
			workingData = make(map[string]interface{})
		}
		if err := setAutoTimestamp(workingData, q.updateTimeField, time.Now(), q.timestampValueType); err != nil {
			return 0, q.reportError("update", err, redactDataKeys(data))
		}
	}

	// 当存在 Inc/Dec 产生的 SET 表达式时，需要构建完整 SQL 通过 RawQueryable 执行
	if len(q.setExprs) > 0 {
		return q.updateWithSetExprs(workingData)
	}

	if q.txExecutor != nil {
		sqlConn, connectionErr := q.transactionConnection()
		if connectionErr != nil {
			return 0, q.reportError("update", connectionErr, nil)
		}
		builder := sqlConn.Builder
		query, values := builder.Update(q.resolveTable(), workingData, q.where)
		values = append(values, q.args...)
		result, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), values...)
		if err != nil {
			return 0, q.reportError("update", err, redactDataKeys(workingData))
		}
		return result.RowsAffected()
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return 0, q.reportError("update", stateErr, nil)
	}
	defer release()
	var affected int64
	var err error
	if cc, ok := connection.(ContextualConnection); ok {
		affected, err = cc.UpdateContext(q.context(), q.resolveTable(), workingData, q.where, q.args)
	} else {
		affected, err = connection.Update(q.resolveTable(), workingData, q.where, q.args)
	}
	if err != nil {
		return 0, q.reportError("update", err, redactDataKeys(workingData))
	}
	return affected, nil
}

// updateWithSetExprs 当查询包含 Inc/Dec 产生的 SET 表达式时，构建完整 UPDATE SQL。
// 将 data 中的键值对与 setExprs 合并为 SET 子句，通过 RawQueryable.Execute() 或事务连接执行。
func (q *Query) updateWithSetExprs(data map[string]interface{}) (int64, error) {
	tableName := q.resolveTable()
	var sqlBuilder strings.Builder
	allArgs := make([]interface{}, 0)

	b := q.builder()
	quote := func(name string) string {
		if b != nil {
			return b.QuoteIdentifier(name)
		}
		return name
	}

	sqlBuilder.WriteString("UPDATE ")
	sqlBuilder.WriteString(quote(tableName))
	sqlBuilder.WriteString(" SET ")

	// 先写入 data 中的键值对（字段名经方言引用，规避保留字冲突）
	setParts := make([]string, 0, len(data)+len(q.setExprs))
	for _, field := range sortedDatabaseKeys(data) {
		setParts = append(setParts, fmt.Sprintf("%s = ?", quote(field)))
		allArgs = append(allArgs, data[field])
	}
	// 自增自减字段和步长均由框架生成，字段按方言引用，步长继续使用绑定参数。
	for _, expression := range q.setExprs {
		quotedField := quote(expression.field)
		setParts = append(setParts, fmt.Sprintf("%s = %s %s ?", quotedField, quotedField, expression.operator))
		allArgs = append(allArgs, expression.amount)
	}

	sqlBuilder.WriteString(strings.Join(setParts, ", "))

	if len(q.where) > 0 {
		sqlBuilder.WriteString(" WHERE ")
		sqlBuilder.WriteString(strings.Join(q.where, " AND "))
		allArgs = append(allArgs, q.args...)
	}

	if q.txExecutor != nil {
		affected, err := q.executeSQLInTx(sqlBuilder.String(), allArgs...)
		if err != nil {
			ctx := redactDataKeys(data)
			ctx["setExprs"] = q.setExprs
			return 0, q.reportError("update", err, ctx)
		}
		return affected, nil
	}

	affected, err := q.rawExecute(sqlBuilder.String(), allArgs...)
	if err != nil {
		ctx := redactDataKeys(data)
		ctx["setExprs"] = q.setExprs
		return 0, q.reportError("update", err, ctx)
	}
	return affected, nil
}

// Delete 删除记录。
// 安全检查：禁止无 WHERE 条件的 DELETE，防止误删全表数据。
func (q *Query) Delete() (int64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("delete", err, nil)
	}
	if len(q.where) == 0 {
		return 0, q.reportError("delete", fmt.Errorf("%w: 表 %s 缺少 WHERE 条件", ErrUnsafeFullTableMutation, q.table), nil)
	}

	if q.txExecutor != nil {
		sqlConn, connectionErr := q.transactionConnection()
		if connectionErr != nil {
			return 0, q.reportError("delete", connectionErr, nil)
		}
		builder := sqlConn.Builder
		query := builder.Delete(q.resolveTable(), q.where)
		result, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), q.args...)
		if err != nil {
			return 0, q.reportError("delete", err, nil)
		}
		return result.RowsAffected()
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return 0, q.reportError("delete", stateErr, nil)
	}
	defer release()
	var affected int64
	var err error
	if cc, ok := connection.(ContextualConnection); ok {
		affected, err = cc.DeleteContext(q.context(), q.resolveTable(), q.where, q.args)
	} else {
		affected, err = connection.Delete(q.resolveTable(), q.where, q.args)
	}
	if err != nil {
		return 0, q.reportError("delete", err, nil)
	}
	return affected, nil
}

// Count 统计记录数。
// 当查询含 JOIN/GROUP/HAVING/DISTINCT 时，简单的 COUNT(*) 无法正确表达，
// 会自动对完整查询包一层子查询：SELECT COUNT(*) FROM (<完整查询>) —— 从而：
//   - JOIN：统计连接过滤后的真实行数；
//   - GROUP BY：统计分组数量（分页 total 正确）；
//   - DISTINCT：统计去重后的行数。
func (q *Query) Count() (int64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("count", err, nil)
	}

	if q.needsRawSelect() {
		total, err := q.countAdvanced()
		if err != nil {
			return 0, q.reportError("count", err, nil)
		}
		return total, nil
	}

	if q.txExecutor != nil {
		sqlConn, connectionErr := q.transactionConnection()
		if connectionErr != nil {
			return 0, q.reportError("count", connectionErr, nil)
		}
		builder := sqlConn.Builder
		sqlStr := builder.Count(q.resolveTable(), q.where)
		rows, err := q.querySQLInTx(sqlStr, q.args...)
		if err != nil {
			return 0, q.reportError("count", err, nil)
		}
		if len(rows) == 0 {
			return 0, nil
		}
		for _, val := range rows[0] {
			return parseCountValue(val)
		}
		return 0, nil
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return 0, q.reportError("count", stateErr, nil)
	}
	defer release()
	var total int64
	var err error
	if cc, ok := connection.(ContextualConnection); ok {
		total, err = cc.CountContext(q.context(), q.resolveTable(), q.where, q.args)
	} else {
		total, err = connection.Count(q.resolveTable(), q.where, q.args)
	}
	if err != nil {
		return 0, q.reportError("count", err, nil)
	}
	return total, nil
}

// countAdvanced 通过对完整查询包裹子查询来统计 JOIN/GROUP/HAVING/DISTINCT 查询的真实行数。
func (q *Query) countAdvanced() (int64, error) {
	sub := q.clone()
	// 子查询无需排序/分页/锁，去掉以保证语义正确并兼容各方言（如 SQL Server 子查询排序限制）。
	sub.order = ""
	sub.limit = 0
	sub.offset = 0
	sub.lockMode = ""
	// 非 DISTINCT 时投影降级为常量，避免 JOIN 下派生表出现重复列名；
	// DISTINCT 必须保留原字段以正确去重。
	if !sub.distinct {
		sub.fields = "1"
	}

	subSQL, subArgs, err := sub.BuildSelectSQL()
	if err != nil {
		return 0, err
	}
	countSQL := "SELECT COUNT(*) AS tg_count FROM (" + subSQL + ") tg_count_sub"

	if q.txExecutor != nil {
		rows, err := q.querySQLInTx(countSQL, subArgs...)
		if err != nil {
			return 0, err
		}
		return firstCountValue(rows)
	}

	rows, err := q.rawQuery(countSQL, subArgs...)
	if err != nil {
		return 0, err
	}
	return firstCountValue(rows)
}

// firstCountValue 从计数查询结果中提取计数值，兼容不同驱动的列名/类型。
func firstCountValue(rows []map[string]interface{}) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	if val, ok := rows[0]["tg_count"]; ok {
		return parseCountValue(val)
	}
	if len(rows[0]) != 1 {
		return 0, fmt.Errorf("%w: Count 结果缺少唯一计数列", ErrInvalidDatabaseRow)
	}
	for _, val := range rows[0] {
		return parseCountValue(val)
	}
	return 0, fmt.Errorf("%w: Count 结果为空", ErrInvalidDatabaseRow)
}

// querySQLInTx 在事务内执行原生 SQL 查询，并转换为行数据映射切片。
func (q *Query) querySQLInTx(sqlStr string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := q.txExecutor.QueryContext(q.context(), q.rebind(sqlStr), args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// executeSQLInTx 在事务内执行写操作，并返回受影响的行数。
func (q *Query) executeSQLInTx(query string, args ...interface{}) (int64, error) {
	result, err := q.txExecutor.ExecContext(q.context(), q.rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// parseCountValue 精确解析 Count 返回值，拒绝小数、负数、溢出和格式错误。
func parseCountValue(val interface{}) (int64, error) {
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("%w: 无法把 %T(%v) 解析为非负 int64", ErrInvalidAggregateValue, val, val)
	}
	switch v := val.(type) {
	case int64:
		if v < 0 {
			return invalid()
		}
		return v, nil
	case int:
		if v < 0 {
			return invalid()
		}
		return int64(v), nil
	case int8:
		if v < 0 {
			return invalid()
		}
		return int64(v), nil
	case int16:
		if v < 0 {
			return invalid()
		}
		return int64(v), nil
	case int32:
		if v < 0 {
			return invalid()
		}
		return int64(v), nil
	case uint:
		if uint64(v) > math.MaxInt64 {
			return invalid()
		}
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return invalid()
		}
		return int64(v), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= math.Exp2(63) || math.Trunc(v) != v {
			return invalid()
		}
		return int64(v), nil
	case float32:
		value := float64(v)
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= math.Exp2(63) || math.Trunc(value) != value {
			return invalid()
		}
		return int64(value), nil
	case []byte:
		parsed, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		if err != nil || parsed < 0 {
			return invalid()
		}
		return parsed, nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || parsed < 0 {
			return invalid()
		}
		return parsed, nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil || parsed < 0 {
			return invalid()
		}
		return parsed, nil
	}
	return invalid()
}

// Paginate 分页查询。
// 对应 ThinkPHP 的 Db::name('user')->paginate(10)
func (q *Query) Paginate(page, pageSize int) (*Paginator, error) {
	if err := q.ensureValid(); err != nil {
		return nil, err
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}

	total, err := q.Count()
	if err != nil {
		return nil, err
	}

	query := q.clone()
	query.Page(page, pageSize)
	list, err := query.Select()
	if err != nil {
		return nil, err
	}

	return buildPaginator(list, total, page, pageSize)
}
