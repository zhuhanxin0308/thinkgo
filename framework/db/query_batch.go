package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// maxBindParams 单条预编译语句的最大占位符数量（以 MySQL 的 65535 上限为基准，留有余量）。
// 批量插入会据此自动分批，避免超过驱动占位符上限直接失败。
const maxBindParams = 60000

// InsertAll 批量插入多条记录。
// 对应 ThinkPHP 的 Db::name('user')->insertAll($data)
// 当占位符总数（行数×列数）超过驱动上限时自动分批；多批写入会包裹在事务中以保证原子性。
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

	// 提取字段名（以第一条数据为基准），并按字段名排序保证占位符顺序稳定可预期。
	fields := make([]string, 0, len(dataList[0]))
	for field := range dataList[0] {
		if err := validateIdentifier(field); err != nil {
			return 0, q.reportError("insert_all", fmt.Errorf("unsafe field: %w", err), nil)
		}
		fields = append(fields, field)
	}
	sort.Strings(fields)

	// 校验后续每行字段集合与首行一致，避免静默丢列或错位写入脏数据。
	for index, data := range dataList {
		if len(data) != len(fields) {
			return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行字段数量与首行不一致，批量插入要求所有行字段集合相同", index), nil)
		}
		for _, field := range fields {
			if _, ok := data[field]; !ok {
				return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行缺少字段 %q，批量插入要求所有行字段集合相同", index, field), nil)
			}
		}
	}

	// 按占位符预算计算每批行数。
	batchRows := maxBindParams / len(fields)
	if batchRows < 1 {
		batchRows = 1
	}

	// 单批可容纳：保持原有单语句路径（无需开事务）。
	if len(dataList) <= batchRows || q.txExecutor != nil {
		return q.insertAllBatched(sqlConn, fields, dataList, batchRows)
	}

	// 多批且不在事务中：包裹事务保证整体原子性，避免部分批次成功部分失败。
	tx, err := q.db.Begin()
	if err != nil {
		return 0, q.reportError("insert_all", err, nil)
	}
	txQuery := q.clone()
	txQuery.txExecutor = tx.tx
	affected, err := txQuery.insertAllBatched(sqlConn, fields, dataList, batchRows)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, q.reportError("insert_all", err, nil)
	}
	return affected, nil
}

// insertAllBatched 按 batchRows 分批执行批量插入，累计影响行数。
func (q *Query) insertAllBatched(sqlConn *SQLConnection, fields []string, dataList []map[string]interface{}, batchRows int) (int64, error) {
	tableName := q.resolveTable()
	quotedFields := make([]string, len(fields))
	for i, field := range fields {
		quotedFields[i] = sqlConn.Builder.QuoteIdentifier(field)
	}

	var total int64
	for start := 0; start < len(dataList); start += batchRows {
		end := start + batchRows
		if end > len(dataList) {
			end = len(dataList)
		}
		chunk := dataList[start:end]

		var sqlBuilder strings.Builder
		sqlBuilder.WriteString("INSERT INTO ")
		sqlBuilder.WriteString(sqlConn.Builder.QuoteIdentifier(tableName))
		sqlBuilder.WriteString(" (")
		sqlBuilder.WriteString(strings.Join(quotedFields, ", "))
		sqlBuilder.WriteString(") VALUES ")

		allArgs := make([]interface{}, 0, len(chunk)*len(fields))
		for i, data := range chunk {
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

		finalSQL := sqlConn.Builder.Rebind(sqlBuilder.String())
		var result sql.Result
		var err error
		if q.txExecutor != nil {
			result, err = q.txExecutor.ExecContext(q.context(), finalSQL, allArgs...)
		} else {
			result, err = sqlConn.DB.ExecContext(q.context(), finalSQL, allArgs...)
		}
		if err != nil {
			return 0, q.reportError("insert_all", err, nil)
		}
		if affected, err := result.RowsAffected(); err == nil {
			total += affected
		}
	}
	return total, nil
}

// ChunkById 基于主键游标分批遍历，避免大表 OFFSET 深分页的性能衰减与遍历中数据增删导致的漏读/重复。
// 每批按主键升序取 count 条，下一批从上一批最大主键之后继续。回调返回 false 可提前终止。
// pkField 必须是有序、唯一的主键列（默认 "id"）。
func (q *Query) ChunkById(count int, pkField string, callback func(rows []map[string]interface{}) bool) error {
	if err := q.ensureValid(); err != nil {
		return q.reportError("chunk_by_id", err, nil)
	}
	if count <= 0 {
		count = 100
	}
	if pkField == "" {
		pkField = "id"
	}
	if err := validateIdentifier(pkField); err != nil {
		return q.reportError("chunk_by_id", fmt.Errorf("unsafe pk field: %w", err), nil)
	}

	var lastID interface{}
	for {
		cloned := q.clone()
		if lastID != nil {
			cloned.WhereField(pkField, ">", lastID)
		}
		// 主键升序 + LIMIT 实现游标分页（不使用 OFFSET）。
		cloned.order = pkField
		cloned.limit = count
		cloned.offset = 0

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
		next, ok := rows[len(rows)-1][pkField]
		if !ok || next == nil {
			// 主键列缺失则无法继续游标，避免死循环。
			break
		}
		lastID = next
	}
	return nil
}

// Chunk 分块查询，每次查询 count 条记录并执行回调。
// 回调返回 false 可提前终止遍历。
// 注意：基于 OFFSET 实现，遍历期间若有数据增删可能漏读/重复；大表深分页性能随页码下降，
// 这类场景建议改用 ChunkById（基于主键游标）。
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
