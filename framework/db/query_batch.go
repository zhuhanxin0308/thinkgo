package db

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

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
	workingList := make([]map[string]interface{}, len(dataList))
	for index, data := range dataList {
		if len(data) == 0 {
			return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行数据不能为空，批量插入要求每行至少包含一个字段", index), nil)
		}
		workingList[index] = cloneDatabaseMap(data)
	}

	var sqlConn *SQLConnection
	if q.txExecutor != nil {
		var connectionErr error
		sqlConn, connectionErr = q.transactionConnection()
		if connectionErr != nil {
			return 0, q.reportError("insert_all", connectionErr, nil)
		}
	} else {
		connection, connectionErr := q.db.connectionSnapshot()
		var ok bool
		sqlConn, ok = connection.(*SQLConnection)
		if connectionErr != nil || !ok || sqlConn == nil {
			return 0, q.reportError("insert_all", fmt.Errorf("%w: 批量插入要求 SQL 连接", ErrInvalidQuery), nil)
		}
	}
	if err := sqlConn.validateContext(q.context()); err != nil {
		return 0, q.reportError("insert_all", err, nil)
	}

	// 对每条数据补齐时间戳
	if q.autoTimestamp {
		now := time.Now()
		for index, data := range workingList {
			if err := setAutoTimestamp(data, q.createTimeField, now, q.timestampValueType); err != nil {
				return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行创建时间戳失败: %w", index, err), nil)
			}
			if err := setAutoTimestamp(data, q.updateTimeField, now, q.timestampValueType); err != nil {
				return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行更新时间戳失败: %w", index, err), nil)
			}
		}
	}

	// 提取字段名（以第一条数据为基准），并按字段名排序保证占位符顺序稳定可预期。
	fields := make([]string, 0, len(workingList[0]))
	for field := range workingList[0] {
		if err := validateIdentifier(field); err != nil {
			return 0, q.reportError("insert_all", fmt.Errorf("unsafe field: %w", err), nil)
		}
		fields = append(fields, field)
	}
	sort.Strings(fields)

	// 校验后续每行字段集合与首行一致，避免静默丢列或错位写入脏数据。
	for index, data := range workingList {
		if len(data) != len(fields) {
			return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行字段数量与首行不一致，批量插入要求所有行字段集合相同", index), nil)
		}
		for _, field := range fields {
			if _, ok := data[field]; !ok {
				return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行缺少字段 %q，批量插入要求所有行字段集合相同", index, field), nil)
			}
		}
	}

	// 按方言占位符预算计算每批行数，避免 SQL Server/SQLite 等低上限驱动拒绝执行。
	batchRows := insertAllBatchRows(sqlConn.Builder, len(fields))
	if batchRows < 1 {
		return 0, q.reportError("insert_all", fmt.Errorf("%w: 单行字段数超过方言绑定参数上限", ErrInvalidDatabaseConfig), nil)
	}

	// 单批可容纳：保持原有单语句路径（无需开事务）。
	if q.txExecutor != nil {
		return q.insertAllBatched(sqlConn, fields, workingList, batchRows)
	}
	if len(workingList) <= batchRows {
		connection, release, connectionErr := q.db.acquireConnection()
		if connectionErr != nil {
			return 0, q.reportError("insert_all", connectionErr, nil)
		}
		defer release()
		leasedSQLConnection, ok := connection.(*SQLConnection)
		if !ok || leasedSQLConnection == nil {
			return 0, q.reportError("insert_all", fmt.Errorf("%w: 批量插入要求 SQL 连接", ErrInvalidQuery), nil)
		}
		return q.insertAllBatched(leasedSQLConnection, fields, workingList, batchRows)
	}

	// 多批且不在事务中：包裹事务保证整体原子性，避免部分批次成功部分失败。
	tx, err := q.db.BeginTx(q.context(), nil)
	if err != nil {
		return 0, q.reportError("insert_all", err, nil)
	}
	txQuery := q.clone()
	txQuery.txExecutor = tx.tx
	txQuery.txOwner = tx
	txQuery.ctx = tx.ctx
	affected, err := txQuery.insertAllBatched(tx.connection, fields, workingList, batchRows)
	if err != nil {
		return 0, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return 0, q.reportError("insert_all", err, nil)
	}
	return affected, nil
}

// insertAllBatched 按 batchRows 分批执行批量插入，累计影响行数。
func (q *Query) insertAllBatched(sqlConn *SQLConnection, fields []string, dataList []map[string]interface{}, batchRows int) (int64, error) {
	tableName := q.resolveTable()

	var total int64
	for start := 0; start < len(dataList); start += batchRows {
		end := start + batchRows
		if end > len(dataList) {
			end = len(dataList)
		}
		chunk := dataList[start:end]
		batchSQL, allArgs := buildBatchInsertStatement(sqlConn.Builder, tableName, fields, chunk)
		finalSQL := sqlConn.Builder.Rebind(batchSQL)
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
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, q.reportError("insert_all", err, nil)
		}
		total, err = accumulateAffectedRows(total, affected)
		if err != nil {
			return 0, q.reportError("insert_all", err, nil)
		}
	}
	return total, nil
}

// buildBatchInsertStatement 优先让特殊方言接管批量语法；
// 其余方言统一使用参数化的多行 VALUES，并保持字段与参数的稳定顺序。
func buildBatchInsertStatement(build Builder, table string, fields []string, rows []map[string]interface{}) (string, []interface{}) {
	if dialect, ok := build.(BatchInsertBuilder); ok {
		return dialect.InsertBatch(table, fields, rows)
	}

	quotedFields := make([]string, len(fields))
	for index, field := range fields {
		quotedFields[index] = build.QuoteIdentifier(field)
	}
	var sqlBuilder strings.Builder
	sqlBuilder.WriteString("INSERT INTO ")
	sqlBuilder.WriteString(build.QuoteIdentifier(table))
	sqlBuilder.WriteString(" (")
	sqlBuilder.WriteString(strings.Join(quotedFields, ", "))
	sqlBuilder.WriteString(") VALUES ")

	values := make([]interface{}, 0, len(rows)*len(fields))
	for rowIndex, row := range rows {
		if rowIndex > 0 {
			sqlBuilder.WriteString(", ")
		}
		sqlBuilder.WriteString("(")
		for fieldIndex, field := range fields {
			if fieldIndex > 0 {
				sqlBuilder.WriteString(", ")
			}
			sqlBuilder.WriteString("?")
			values = append(values, row[field])
		}
		sqlBuilder.WriteString(")")
	}
	return sqlBuilder.String(), values
}

// accumulateAffectedRows 安全累计批量写入影响行数，拒绝驱动异常值和整数溢出。
func accumulateAffectedRows(total, affected int64) (int64, error) {
	if total < 0 || affected < 0 || total > math.MaxInt64-affected {
		return 0, fmt.Errorf("%w: 批量写入影响行数非法", ErrInvalidAggregateValue)
	}
	return total + affected, nil
}

func insertAllBatchRows(builder Builder, fieldCount int) int {
	if fieldCount <= 0 {
		return 1
	}
	maxParams := maxBindParamsForBuilder(builder)
	if fieldCount > maxParams {
		return 0
	}
	rows := maxParams / fieldCount
	if rows < 1 {
		return 1
	}
	return rows
}

func maxBindParamsForBuilder(builder Builder) int {
	if isNilDatabaseDependency(builder) {
		return 0
	}
	return builder.MaxBindParams()
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
	if callback == nil {
		return q.reportError("chunk_by_id", fmt.Errorf("%w: ChunkById 回调不能为空", ErrInvalidQuery), nil)
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
		var next interface{}
		if len(rows) == count {
			var ok bool
			next, ok = rows[len(rows)-1][pkField]
			if !ok || next == nil {
				return q.reportError("chunk_by_id", fmt.Errorf("%w: 满批结果缺少游标字段 %q", ErrInvalidDatabaseRow, pkField), nil)
			}
			if lastID != nil {
				comparison, compareErr := compareDatabaseCursor(lastID, next)
				if compareErr != nil {
					return q.reportError("chunk_by_id", compareErr, nil)
				}
				if comparison >= 0 {
					return q.reportError("chunk_by_id", fmt.Errorf("%w: 游标 %v 未严格递增到 %v", ErrInvalidDatabaseRow, lastID, next), nil)
				}
			}
		}
		if !callback(rows) {
			break
		}
		if len(rows) < count {
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
	if callback == nil {
		return q.reportError("chunk", fmt.Errorf("%w: Chunk 回调不能为空", ErrInvalidQuery), nil)
	}

	page := 1
	for {
		cloned := q.clone()
		cloned.limit = count
		if page > 1 && page-1 > int(^uint(0)>>1)/count {
			return q.reportError("chunk", fmt.Errorf("%w: Chunk 偏移量溢出", ErrInvalidPagination), nil)
		}
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
