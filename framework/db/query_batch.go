package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// InsertAll 批量插入多条记录。
// 对应 ThinkPHP 的 Db::name('user')->insertAll($data)
func (q *Query) InsertAll(dataList []map[string]interface{}) (int64, error) {
	if err := q.ensureValid(); err != nil {
		return 0, q.reportError("insert_all", err, nil)
	}
	if len(dataList) == 0 {
		return 0, nil
	}

	sqlConn, ok := q.db.connection.(*SQLConnection)
	if !ok {
		return 0, fmt.Errorf("batch insert requires SQL connection")
	}

	// 对每条数据补齐时间戳
	if q.autoTimestamp {
		now := time.Now()
		for _, data := range dataList {
			setAutoTimestamp(data, q.createTimeField, now, q.timestampValueType)
			setAutoTimestamp(data, q.updateTimeField, now, q.timestampValueType)
		}
	}

	// 提取字段名（以第一条数据为基准）
	fields := make([]string, 0, len(dataList[0]))
	for field := range dataList[0] {
		if err := validateIdentifier(field); err != nil {
			return 0, q.reportError("insert_all", fmt.Errorf("unsafe field: %w", err), nil)
		}
		fields = append(fields, field)
	}

	// 构建批量 INSERT SQL
	tableName := q.resolveTable()
	var sqlBuilder strings.Builder
	sqlBuilder.WriteString("INSERT INTO ")
	sqlBuilder.WriteString(tableName)
	sqlBuilder.WriteString(" (")
	sqlBuilder.WriteString(strings.Join(fields, ", "))
	sqlBuilder.WriteString(") VALUES ")

	allArgs := make([]interface{}, 0, len(dataList)*len(fields))
	for i, data := range dataList {
		if i > 0 {
			sqlBuilder.WriteString(", ")
		}
		sqlBuilder.WriteString("(")
		for j, field := range fields {
			if j > 0 {
				sqlBuilder.WriteString(", ")
			}
			sqlBuilder.WriteString("?")
			allArgs = append(allArgs, data[field])
		}
		sqlBuilder.WriteString(")")
	}

	// 批量 INSERT 以 ? 占位符构建，执行前按方言转换占位符。
	finalSQL := sqlConn.Builder.Rebind(sqlBuilder.String())
	var result sql.Result
	var err error
	if q.txExecutor != nil {
		result, err = q.txExecutor.Exec(finalSQL, allArgs...)
	} else {
		result, err = sqlConn.DB.Exec(finalSQL, allArgs...)
	}
	if err != nil {
		return 0, q.reportError("insert_all", err, nil)
	}
	return result.RowsAffected()
}

// Chunk 分块查询，每次查询 count 条记录并执行回调。
// 回调返回 false 可提前终止遍历。
// 对应 ThinkPHP 的 Db::name('user')->chunk(100, function($users) { ... })
func (q *Query) Chunk(count int, callback func(rows []map[string]interface{}) bool) error {
	if err := q.ensureValid(); err != nil {
		return q.reportError("chunk", err, nil)
	}
	if count <= 0 {
		count = 100
	}

	page := 1
	for {
		cloned := q.clone()
		cloned.limit = count
		cloned.offset = (page - 1) * count

		rows, err := cloned.Select()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}

		if !callback(rows) {
			break
		}

		if len(rows) < count {
			break
		}
		page++
	}
	return nil
}
