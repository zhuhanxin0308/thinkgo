package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Find 查询单条记录。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->find()
// 在克隆上设置 LIMIT 1，避免污染调用方的查询构建器状态（终端方法应可重复执行）。
func (q *Query) Find() (map[string]interface{}, error) {
	// Find 只覆盖终端 LIMIT，不会修改查询切片，结构体副本足以隔离调用方状态。
	cloned := *q
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
	q, cancel := q.withOperationContext()
	defer cancel()
	defer q.traceOperation("select")()

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
		if err := validateMaterializedRows(rows); err != nil {
			return nil, q.reportError("select", err, nil)
		}
		return rows, nil
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return nil, q.reportError("select", stateErr, nil)
	}
	defer release()
	predicate, predicateErr := q.operationPredicate()
	if predicateErr != nil {
		return nil, q.reportError("select", predicateErr, nil)
	}
	rows, err := connection.Select(q.context(), newSelectRequest(
		q.resolveTable(), q.fields, predicate, q.insertPrimaryKey, q.order, q.limit, q.offset, q.aggregateExpression, q.modelPrimaryKey,
	))
	if err != nil {
		return nil, q.reportError("select", err, map[string]interface{}{
			"fields": q.fields,
		})
	}
	if err := validateMaterializedRows(rows); err != nil {
		return nil, q.reportError("select", err, nil)
	}
	return rows, nil
}

// Each 按行消费支持流式能力的查询结果，避免把完整结果集物化到内存。
// 回调返回 false 时停止读取；调用方应使用 WithContext 为长查询设置取消边界。
// SQL 和 MongoDB 使用框架统一入口；其它连接必须使用各自的游标 API。
func (q *Query) Each(callback func(row map[string]interface{}) bool) error {
	if err := q.ensureValid(); err != nil {
		return q.reportError("each", err, nil)
	}
	if callback == nil {
		return q.reportError("each", fmt.Errorf("%w: 流式查询回调不能为空", ErrInvalidQuery), nil)
	}
	q, cancel := q.withOperationContext()
	defer cancel()
	defer q.traceOperation("each")()

	if q.txExecutor != nil {
		sqlCloned := q.clone()
		sqlStr, args, err := sqlCloned.BuildSelectSQL()
		if err != nil {
			return q.reportError("each", err, nil)
		}
		rows, err := q.txExecutor.QueryContext(q.context(), q.rebind(sqlStr), args...)
		if err != nil {
			return q.reportError("each", err, nil)
		}
		return q.reportError("each", scanSQLRowsEach(rows, q.builder(), q.location(), callback), nil)
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return q.reportError("each", stateErr, nil)
	}
	defer release()

	if q.needsRawSelect() {
		sqlConnection, ok := connection.(*SQLConnection)
		if !ok || sqlConnection == nil {
			return q.reportError("each", fmt.Errorf("%w: 高级流式查询要求 SQL 连接", ErrUnsupportedFeature), nil)
		}
		sqlStr, args, err := q.BuildSelectSQL()
		if err != nil {
			return q.reportError("each", err, nil)
		}
		err = sqlConnection.QueryEachContext(q.context(), sqlStr, callback, args...)
		return q.reportError("each", err, nil)
	}

	streamingConnection, ok := connection.(RowStreamingConnection)
	if !ok {
		return q.reportError("each", fmt.Errorf("%w: 当前数据库连接不支持流式查询", ErrUnsupportedFeature), nil)
	}

	predicate, predicateErr := q.operationPredicate()
	if predicateErr != nil {
		return q.reportError("each", predicateErr, nil)
	}
	err := streamingConnection.SelectEach(q.context(), newSelectRequest(
		q.resolveTable(), q.fields, predicate, q.insertPrimaryKey, q.order, q.limit, q.offset, q.aggregateExpression, q.modelPrimaryKey,
	), callback)
	if err != nil {
		return q.reportError("each", err, map[string]interface{}{"fields": q.fields})
	}
	return nil
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

// rawQuery 要求连接完整支持 context，避免高级查询的超时和取消被静默丢弃。
func (q *Query) rawQuery(sqlStr string, args ...interface{}) ([]map[string]interface{}, error) {
	connection, release, err := q.db.acquireConnection()
	if err != nil {
		return nil, err
	}
	defer release()
	if cc, ok := connection.(ContextualRawQueryable); ok {
		rows, err := cc.QueryContext(q.context(), sqlStr, args...)
		if err != nil {
			return nil, err
		}
		return rows, validateMaterializedRows(rows)
	}
	return nil, fmt.Errorf("%w: 高级查询要求驱动实现 ContextualRawQueryable", ErrUnsupportedFeature)
}

// rawExecute 要求连接完整支持 context，避免高级写操作的超时和取消被静默丢弃。
func (q *Query) rawExecute(sqlStr string, args ...interface{}) (int64, error) {
	connection, release, err := q.db.acquireConnection()
	if err != nil {
		return 0, err
	}
	defer release()
	if cc, ok := connection.(ContextualRawQueryable); ok {
		return cc.ExecuteContext(q.context(), sqlStr, args...)
	}
	return 0, fmt.Errorf("%w: 高级写操作要求驱动实现 ContextualRawQueryable", ErrUnsupportedFeature)
}

// Insert inserts one record and returns the affected-row count, matching ThinkPHP.
func (q *Query) Insert(data map[string]interface{}) (int64, error) {
	result, err := q.insertResult(data, false)
	if err != nil {
		return 0, err
	}
	return result.Affected, nil
}

// InsertGetId inserts one record and returns the real identifier reported by the driver.
func (q *Query) InsertGetId(data map[string]interface{}) (interface{}, error) {
	result, err := q.insertResult(data, true)
	if err != nil {
		return nil, err
	}
	return result.InsertedID()
}

// Save updates when the query contains a predicate and inserts otherwise.
// Passing true forces insert; more than one flag is invalid.
func (q *Query) Save(data map[string]interface{}, forceInsert ...bool) (int64, error) {
	if len(forceInsert) > 1 {
		return 0, q.reportError("save", fmt.Errorf("%w: Save accepts at most one forceInsert flag", ErrInvalidQuery), nil)
	}
	if (len(forceInsert) == 1 && forceInsert[0]) || len(q.where) == 0 {
		return q.Insert(data)
	}
	return q.Update(data)
}

func (q *Query) insertResult(data map[string]interface{}, wantID bool) (InsertResult, error) {
	if err := q.ensureValid(); err != nil {
		return InsertResult{}, q.reportError("insert", err, nil)
	}
	if len(data) == 0 {
		return InsertResult{}, q.reportError("insert", fmt.Errorf("插入数据不能为空"), nil)
	}
	if err := validateDataKeys(data); err != nil {
		wrappedErr := fmt.Errorf("unsafe insert fields: %w", err)
		return InsertResult{}, q.reportError("insert", wrappedErr, redactDataKeys(data))
	}
	if len(q.setExprs) > 0 {
		return InsertResult{}, q.reportError("insert", fmt.Errorf("%w: Insert 不支持 Inc/Dec 表达式", ErrInvalidQuery), nil)
	}
	q, cancel := q.withOperationContext()
	defer cancel()
	defer q.traceOperation("insert")()
	workingData := normalizeDatabaseWriteMap(data, q.location())

	if q.autoTimestamp {
		now := q.now()
		if err := setAutoTimestamp(workingData, q.createTimeField, now, q.timestampValueType); err != nil {
			return InsertResult{}, q.reportError("insert", err, redactDataKeys(data))
		}
		if err := setAutoTimestamp(workingData, q.updateTimeField, now, q.timestampValueType); err != nil {
			return InsertResult{}, q.reportError("insert", err, redactDataKeys(data))
		}
	}
	if err := q.validateQueryStatementArguments(len(workingData)); err != nil {
		return InsertResult{}, q.reportError("insert", err, nil)
	}

	if q.txExecutor != nil {
		result, err := q.insertResultInTx(newInsertRequest(q.resolveTable(), workingData, q.insertPrimaryKey, wantID, q.modelPrimaryKey))
		if err != nil {
			return InsertResult{}, q.reportError("insert", err, redactDataKeys(workingData))
		}
		result.Data = cloneDatabaseMap(workingData)
		return result, nil
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return InsertResult{}, q.reportError("insert", stateErr, nil)
	}
	defer release()
	result, err := connection.Insert(q.context(), newInsertRequest(q.resolveTable(), workingData, q.insertPrimaryKey, wantID, q.modelPrimaryKey))
	if err != nil {
		return InsertResult{}, q.reportError("insert", err, redactDataKeys(workingData))
	}
	if err := result.Validate(); err != nil {
		return InsertResult{}, q.reportError("insert", err, nil)
	}
	result.Data = cloneDatabaseMap(workingData)
	return result, nil
}

func (q *Query) insertResultInTx(request InsertRequest) (InsertResult, error) {
	sqlConnection, err := q.transactionConnection()
	if err != nil {
		return InsertResult{}, err
	}
	builder := sqlConnection.Builder
	data := request.Data()
	if request.WantsID() && !builder.SupportsLastInsertId() {
		query, values, ok := builder.InsertReturning(request.Table(), data, request.PrimaryKey())
		if !ok {
			return InsertResult{}, ErrInsertIDUnavailable
		}
		operationResult := InsertResult{Affected: 1, Data: data}
		if len(values) > 0 {
			if output, isOutput := values[len(values)-1].(sql.Out); isOutput {
				if _, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), values...); err != nil {
					return InsertResult{}, err
				}
				id, err := sqlOutputValue(output.Dest)
				if err != nil {
					return InsertResult{}, err
				}
				operationResult.ID, operationResult.IDKnown = id, true
				return operationResult, operationResult.Validate()
			}
		}
		var id interface{}
		if err := q.txExecutor.QueryRowContext(q.context(), builder.Rebind(query), values...).Scan(&id); err != nil {
			return InsertResult{}, err
		}
		operationResult.ID, operationResult.IDKnown = id, true
		return operationResult, operationResult.Validate()
	}

	query, values := builder.Insert(request.Table(), data)
	sqlResult, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), values...)
	if err != nil {
		return InsertResult{}, err
	}
	affected, err := sqlResult.RowsAffected()
	if err != nil {
		return InsertResult{}, err
	}
	operationResult := InsertResult{Affected: affected, Data: data}
	if request.WantsID() {
		operationResult.ID, err = sqlResult.LastInsertId()
		operationResult.IDKnown = err == nil
		if err != nil {
			return InsertResult{}, err
		}
	}
	return operationResult, operationResult.Validate()
}

// Update 更新记录。
// 安全检查：禁止无 WHERE 条件的 UPDATE，防止误更新全表数据。
func (q *Query) Update(data map[string]interface{}) (int64, error) {
	result, err := q.UpdateResult(data)
	if err != nil {
		return 0, err
	}
	return result.Count(), nil
}

// UpdateResult preserves all count semantics exposed by the selected driver.
func (q *Query) UpdateResult(data map[string]interface{}) (UpdateResult, error) {
	if err := q.ensureValid(); err != nil {
		return UpdateResult{}, q.reportError("update", err, nil)
	}
	if len(q.where) == 0 {
		return UpdateResult{}, q.reportError("update", fmt.Errorf("%w: 表 %s 缺少 WHERE 条件", ErrUnsafeFullTableMutation, q.table), nil)
	}
	if len(data) == 0 && len(q.setExprs) == 0 {
		return UpdateResult{}, q.reportError("update", fmt.Errorf("更新数据不能为空"), nil)
	}
	if err := validateDataKeys(data); err != nil {
		wrappedErr := fmt.Errorf("unsafe update fields: %w", err)
		return UpdateResult{}, q.reportError("update", wrappedErr, redactDataKeys(data))
	}
	for _, expression := range q.setExprs {
		if _, duplicated := data[expression.field]; duplicated {
			return UpdateResult{}, q.reportError("update", fmt.Errorf("%w: 字段 %q 同时出现在 data 与 Inc/Dec 中", ErrInvalidQuery, expression.field), nil)
		}
	}
	q, cancel := q.withOperationContext()
	defer cancel()
	defer q.traceOperation("update")()
	workingData := normalizeDatabaseWriteMap(data, q.location())

	if q.autoTimestamp {
		if workingData == nil {
			workingData = make(map[string]interface{})
		}
		if err := setAutoTimestamp(workingData, q.updateTimeField, q.now(), q.timestampValueType); err != nil {
			return UpdateResult{}, q.reportError("update", err, redactDataKeys(data))
		}
	}
	if err := q.validateQueryStatementArguments(len(workingData), len(q.setExprs), len(q.args)); err != nil {
		return UpdateResult{}, q.reportError("update", err, nil)
	}

	// 当存在 Inc/Dec 产生的 SET 表达式时，需要构建完整 SQL 通过 RawQueryable 执行
	if len(q.setExprs) > 0 {
		affected, err := q.updateWithSetExprs(workingData)
		return UpdateResult{Affected: affected, Data: cloneDatabaseMap(workingData)}, err
	}

	if q.txExecutor != nil {
		sqlConn, connectionErr := q.transactionConnection()
		if connectionErr != nil {
			return UpdateResult{}, q.reportError("update", connectionErr, nil)
		}
		builder := sqlConn.Builder
		query, values := builder.Update(q.resolveTable(), workingData, q.where)
		values = append(values, q.args...)
		result, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), values...)
		if err != nil {
			return UpdateResult{}, q.reportError("update", err, redactDataKeys(workingData))
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return UpdateResult{}, q.reportError("update", err, nil)
		}
		return UpdateResult{Affected: affected, Data: cloneDatabaseMap(workingData)}, nil
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return UpdateResult{}, q.reportError("update", stateErr, nil)
	}
	defer release()
	predicate, err := q.operationPredicate()
	if err != nil {
		return UpdateResult{}, q.reportError("update", err, nil)
	}
	result, err := connection.Update(q.context(), newUpdateRequest(q.resolveTable(), workingData, predicate, q.insertPrimaryKey, q.modelPrimaryKey))
	if err != nil {
		return UpdateResult{}, q.reportError("update", err, redactDataKeys(workingData))
	}
	if err := result.Validate(); err != nil {
		return UpdateResult{}, q.reportError("update", err, nil)
	}
	result.Data = cloneDatabaseMap(workingData)
	return result, nil
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
	result, err := q.DeleteResult()
	if err != nil {
		return 0, err
	}
	return result.Deleted, nil
}

// DeleteResult preserves driver-specific related-deletion information.
func (q *Query) DeleteResult() (DeleteResult, error) {
	return q.deleteResult(false)
}

// DetachDelete explicitly permits backends such as Neo4j to remove attached
// relationships along with the matched records/nodes.
func (q *Query) DetachDelete() (int64, error) {
	result, err := q.DetachDeleteResult()
	if err != nil {
		return 0, err
	}
	return result.Deleted, nil
}

// DetachDeleteResult preserves both record/node and relationship counters.
func (q *Query) DetachDeleteResult() (DeleteResult, error) {
	return q.deleteResult(true)
}

func (q *Query) deleteResult(detachRelations bool) (DeleteResult, error) {
	if err := q.ensureValid(); err != nil {
		return DeleteResult{}, q.reportError("delete", err, nil)
	}
	if len(q.where) == 0 {
		return DeleteResult{}, q.reportError("delete", fmt.Errorf("%w: 表 %s 缺少 WHERE 条件", ErrUnsafeFullTableMutation, q.table), nil)
	}
	q, cancel := q.withOperationContext()
	defer cancel()
	defer q.traceOperation("delete")()

	if q.txExecutor != nil {
		sqlConn, connectionErr := q.transactionConnection()
		if connectionErr != nil {
			return DeleteResult{}, q.reportError("delete", connectionErr, nil)
		}
		builder := sqlConn.Builder
		query := builder.Delete(q.resolveTable(), q.where)
		result, err := q.txExecutor.ExecContext(q.context(), builder.Rebind(query), q.args...)
		if err != nil {
			return DeleteResult{}, q.reportError("delete", err, nil)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return DeleteResult{}, q.reportError("delete", err, nil)
		}
		return DeleteResult{Deleted: deleted}, nil
	}

	connection, release, stateErr := q.db.acquireConnection()
	if stateErr != nil {
		return DeleteResult{}, q.reportError("delete", stateErr, nil)
	}
	defer release()
	predicate, err := q.operationPredicate()
	if err != nil {
		return DeleteResult{}, q.reportError("delete", err, nil)
	}
	result, err := connection.Delete(q.context(), newDeleteRequest(q.resolveTable(), predicate, q.insertPrimaryKey, detachRelations, q.modelPrimaryKey))
	if err != nil {
		return DeleteResult{}, q.reportError("delete", err, nil)
	}
	if err := result.Validate(); err != nil {
		return DeleteResult{}, q.reportError("delete", err, nil)
	}
	return result, nil
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
	q, cancel := q.withOperationContext()
	defer cancel()
	defer q.traceOperation("count")()

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
	predicate, predicateErr := q.operationPredicate()
	if predicateErr != nil {
		return 0, q.reportError("count", predicateErr, nil)
	}
	total, err := connection.Count(q.context(), newCountRequest(q.resolveTable(), predicate, q.insertPrimaryKey, q.modelPrimaryKey))
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
	sub.lockMode = LockNone
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
	return scanSQLRows(rows, q.builder(), q.location())
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
func (q *Query) Paginate(page, pageSize int) (*Paginator[map[string]any], error) {
	if err := q.ensureValid(); err != nil {
		return nil, err
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > maxQueryResultRows {
		return nil, q.reportError("paginate", fmt.Errorf("%w: pageSize 超过 %d", ErrInvalidPagination, maxQueryResultRows), nil)
	}

	total, err := q.Count()
	if err != nil {
		return nil, err
	}

	query := q.clone()
	query = query.applyPage(page, pageSize)
	if query.err != nil {
		return nil, query.err
	}
	list, err := query.Select()
	if err != nil {
		return nil, err
	}

	return buildPaginator(list, total, page, pageSize)
}
