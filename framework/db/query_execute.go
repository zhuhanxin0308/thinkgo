package db

import (
	"fmt"
	"strings"
	"time"
)

// Find 查询单条记录。
// 对应 ThinkPHP 的 Db::name('user')->where('id', 1)->find()
func (q *Query) Find() (map[string]interface{}, error) {
	q.Limit(1)
	rows, err := q.Select()
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
		return q.selectAdvanced()
	}

	rows, err := q.db.connection.Select(
		q.resolveTable(),
		q.fields,
		q.where,
		q.args,
		q.order,
		q.limit,
		q.offset,
	)
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
	rawConn, ok := q.db.connection.(RawQueryable)
	if !ok {
		return nil, fmt.Errorf("当前数据库驱动不支持高级查询")
	}

	sqlStr, allArgs, err := q.BuildSelectSQL()
	if err != nil {
		return nil, err
	}

	return rawConn.Query(sqlStr, allArgs...)
}

// Insert 插入一条记录。
func (q *Query) Insert(data map[string]interface{}) (int64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("insert", err, nil)
	}
	if err := validateDataKeys(data); err != nil {
		wrappedErr := fmt.Errorf("unsafe insert fields: %w", err)
		return 0, q.reportError("insert", wrappedErr, map[string]interface{}{
			"data": data,
		})
	}

	if q.autoTimestamp {
		now := time.Now()
		setAutoTimestamp(data, q.createTimeField, now, q.timestampValueType)
		setAutoTimestamp(data, q.updateTimeField, now, q.timestampValueType)
	}

	if q.txExecutor != nil {
		sqlConn, ok := q.db.connection.(*SQLConnection)
		if !ok {
			return 0, q.reportError("insert", fmt.Errorf("当前数据库连接不支持事务操作"), nil)
		}
		builder := sqlConn.Builder
		query, values := builder.Insert(q.resolveTable(), data)
		result, err := q.txExecutor.Exec(builder.Rebind(query), values...)
		if err != nil {
			return 0, q.reportError("insert", err, map[string]interface{}{
				"data": data,
			})
		}
		return result.LastInsertId()
	}

	id, err := q.db.connection.Insert(q.resolveTable(), data)
	if err != nil {
		return 0, q.reportError("insert", err, map[string]interface{}{
			"data": data,
		})
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
		return 0, fmt.Errorf("禁止无 WHERE 条件的 UPDATE 操作，防止误更新全表数据（表: %s）", q.table)
	}
	if err := validateDataKeys(data); err != nil {
		wrappedErr := fmt.Errorf("unsafe update fields: %w", err)
		return 0, q.reportError("update", wrappedErr, map[string]interface{}{
			"data": data,
		})
	}

	if q.autoTimestamp {
		setAutoTimestamp(data, q.updateTimeField, time.Now(), q.timestampValueType)
	}

	// 当存在 Inc/Dec 产生的 SET 表达式时，需要构建完整 SQL 通过 RawQueryable 执行
	if len(q.setExprs) > 0 {
		return q.updateWithSetExprs(data)
	}

	if q.txExecutor != nil {
		sqlConn, ok := q.db.connection.(*SQLConnection)
		if !ok {
			return 0, q.reportError("update", fmt.Errorf("当前数据库连接不支持事务操作"), nil)
		}
		builder := sqlConn.Builder
		query, values := builder.Update(q.resolveTable(), data, q.where)
		values = append(values, q.args...)
		result, err := q.txExecutor.Exec(builder.Rebind(query), values...)
		if err != nil {
			return 0, q.reportError("update", err, map[string]interface{}{
				"data": data,
			})
		}
		return result.RowsAffected()
	}

	affected, err := q.db.connection.Update(q.resolveTable(), data, q.where, q.args)
	if err != nil {
		return 0, q.reportError("update", err, map[string]interface{}{
			"data": data,
		})
	}
	return affected, nil
}

// updateWithSetExprs 当查询包含 Inc/Dec 产生的 SET 表达式时，构建完整 UPDATE SQL。
// 将 data 中的键值对与 setExprs 合并为 SET 子句，通过 RawQueryable.Execute() 或事务连接执行。
func (q *Query) updateWithSetExprs(data map[string]interface{}) (int64, error) {
	tableName := q.resolveTable()
	var sqlBuilder strings.Builder
	allArgs := make([]interface{}, 0)

	sqlBuilder.WriteString("UPDATE ")
	sqlBuilder.WriteString(tableName)
	sqlBuilder.WriteString(" SET ")

	// 先写入 data 中的键值对
	setParts := make([]string, 0, len(data)+len(q.setExprs))
	for field, value := range data {
		setParts = append(setParts, fmt.Sprintf("%s = ?", field))
		allArgs = append(allArgs, value)
	}
	// 再追加 Inc/Dec 的 SET 表达式（不需要参数化，因为值是固定整数）
	setParts = append(setParts, q.setExprs...)

	sqlBuilder.WriteString(strings.Join(setParts, ", "))

	if len(q.where) > 0 {
		sqlBuilder.WriteString(" WHERE ")
		sqlBuilder.WriteString(strings.Join(q.where, " AND "))
		allArgs = append(allArgs, q.args...)
	}

	if q.txExecutor != nil {
		affected, err := q.executeSQLInTx(sqlBuilder.String(), allArgs...)
		if err != nil {
			return 0, q.reportError("update", err, map[string]interface{}{
				"data":     data,
				"setExprs": q.setExprs,
			})
		}
		return affected, nil
	}

	rawConn, ok := q.db.connection.(RawQueryable)
	if !ok {
		return 0, fmt.Errorf("Inc/Dec 操作需要支持原生 SQL 执行的数据库连接")
	}

	affected, err := rawConn.Execute(sqlBuilder.String(), allArgs...)
	if err != nil {
		return 0, q.reportError("update", err, map[string]interface{}{
			"data":     data,
			"setExprs": q.setExprs,
		})
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
		return 0, fmt.Errorf("禁止无 WHERE 条件的 DELETE 操作，防止误删全表数据（表: %s）", q.table)
	}

	if q.txExecutor != nil {
		sqlConn, ok := q.db.connection.(*SQLConnection)
		if !ok {
			return 0, q.reportError("delete", fmt.Errorf("当前数据库连接不支持事务操作"), nil)
		}
		builder := sqlConn.Builder
		query := builder.Delete(q.resolveTable(), q.where)
		result, err := q.txExecutor.Exec(builder.Rebind(query), q.args...)
		if err != nil {
			return 0, q.reportError("delete", err, nil)
		}
		return result.RowsAffected()
	}

	affected, err := q.db.connection.Delete(q.resolveTable(), q.where, q.args)
	if err != nil {
		return 0, q.reportError("delete", err, nil)
	}
	return affected, nil
}

// Count 统计记录数。
func (q *Query) Count() (int64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("count", err, nil)
	}

	if q.txExecutor != nil {
		sqlConn, ok := q.db.connection.(*SQLConnection)
		if !ok {
			return 0, q.reportError("count", fmt.Errorf("当前数据库连接不支持事务操作"), nil)
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
			return parseCountValue(val), nil
		}
		return 0, nil
	}

	total, err := q.db.connection.Count(q.resolveTable(), q.where, q.args)
	if err != nil {
		return 0, q.reportError("count", err, nil)
	}
	return total, nil
}

// querySQLInTx 在事务内执行原生 SQL 查询，并转换为行数据映射切片。
func (q *Query) querySQLInTx(sqlStr string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := q.txExecutor.Query(q.rebind(sqlStr), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			var val interface{}
			valBytes, ok := values[i].([]byte)
			if ok {
				val = string(valBytes)
			} else {
				val = values[i]
			}
			row[col] = val
		}
		result = append(result, row)
	}
	return result, nil
}

// executeSQLInTx 在事务内执行写操作，并返回受影响的行数。
func (q *Query) executeSQLInTx(query string, args ...interface{}) (int64, error) {
	result, err := q.txExecutor.Exec(q.rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// parseCountValue 解析 Count 返回的多种可能类型为 int64。
func parseCountValue(val interface{}) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		var i int64
		fmt.Sscanf(string(v), "%d", &i)
		return i
	case string:
		var i int64
		fmt.Sscanf(v, "%d", &i)
		return i
	}
	return 0
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

	return buildPaginator(list, total, page, pageSize), nil
}
