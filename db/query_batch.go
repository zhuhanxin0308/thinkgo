package db

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// 批量协议预算预留固定包头，避免估算值恰好等于驱动上限时越过真实边界。
	batchStatementEnvelopeBytes int64 = 64
	// 为每个参数预留类型、长度和协议封装空间，保证字符串和二进制值采用保守估算。
	batchStatementParameterOverheadBytes int64 = 16
	// time.Time 会按驱动方言转换为文本或时间结构，使用统一上界避免低估。
	batchStatementTimeValueBytes int64 = 64
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
	q, cancel := q.withOperationContext()
	defer cancel()
	defer q.traceOperation("insert_all")()
	workingList := make([]map[string]interface{}, len(dataList))
	for index, data := range dataList {
		if len(data) == 0 {
			return 0, q.reportError("insert_all", fmt.Errorf("第 %d 行数据不能为空，批量插入要求每行至少包含一个字段", index), nil)
		}
		workingList[index] = normalizeDatabaseWriteMap(data, q.location())
	}

	var sqlConn *SQLConnection
	var sqlBuilder Builder
	if q.txExecutor != nil {
		var connectionErr error
		sqlConn, connectionErr = q.transactionConnection()
		if connectionErr != nil {
			return 0, q.reportError("insert_all", connectionErr, nil)
		}
		if err := sqlConn.validateContext(q.context()); err != nil {
			return 0, q.reportError("insert_all", err, nil)
		}
		sqlBuilder = sqlConn.Builder
	} else {
		connectionErr := q.db.WithConnection(func(connection Connection) error {
			leasedSQLConnection, ok := connection.(*SQLConnection)
			if !ok || leasedSQLConnection == nil {
				return fmt.Errorf("%w: 批量插入要求 SQL 连接", ErrInvalidQuery)
			}
			if err := leasedSQLConnection.validateContext(q.context()); err != nil {
				return err
			}
			sqlBuilder = leasedSQLConnection.Builder
			return nil
		})
		if connectionErr != nil {
			return 0, q.reportError("insert_all", connectionErr, nil)
		}
	}

	// 对每条数据补齐时间戳
	if q.autoTimestamp {
		now := q.now()
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
	batchRows := insertAllBatchRows(sqlBuilder, len(fields))
	if batchRows < 1 {
		return 0, q.reportError("insert_all", fmt.Errorf("%w: 单行字段数超过方言绑定参数上限", ErrInvalidDatabaseConfig), nil)
	}
	// 当方言提供协议包预算时，先确认当前输入是否会因字节大小产生额外批次。
	// 这一步决定是否需要事务，避免包大小导致的隐式多批写入漏掉原子性保护。
	if maxBytes := maxBatchStatementBytes(sqlBuilder); maxBytes > 0 && len(workingList) <= batchRows {
		_, _, fittingRows, err := buildBatchInsertChunk(sqlBuilder, q.resolveTable(), fields, workingList, batchRows)
		if err != nil {
			return 0, q.reportError("insert_all", err, nil)
		}
		batchRows = fittingRows
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
	for start := 0; start < len(dataList); {
		end := start + batchRows
		if end > len(dataList) {
			end = len(dataList)
		}
		batchSQL, allArgs, actualRows, err := buildBatchInsertChunk(
			sqlConn.Builder, tableName, fields, dataList[start:end], end-start,
		)
		if err != nil {
			return 0, q.reportError("insert_all", err, nil)
		}
		finalSQL := sqlConn.Builder.Rebind(batchSQL)
		var result sql.Result
		if q.txExecutor != nil {
			result, err = q.txExecutor.ExecContext(q.context(), finalSQL, allArgs...)
		} else {
			result, err = sqlConn.execContext(q.context(), finalSQL, allArgs...)
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
		// actualRows 由包大小预算动态确定，不能继续使用固定 batchRows 推进游标。
		start += actualRows
	}
	return total, nil
}

// buildBatchInsertChunk 在绑定参数和协议包预算的共同约束下选择当前批次的最大行数。
// 使用二分查找减少大批量导入时的 SQL 构建次数，并保持每批尽可能接近上限。
func buildBatchInsertChunk(build Builder, table string, fields []string, rows []map[string]interface{}, maxRows int) (string, []interface{}, int, error) {
	if maxRows <= 0 || len(rows) == 0 {
		return "", nil, 0, fmt.Errorf("%w: 批量插入批次不能为空", ErrInvalidQuery)
	}
	if maxRows > len(rows) {
		maxRows = len(rows)
	}
	maxBytes := maxBatchStatementBytes(build)
	if maxBytes <= 0 {
		query, values := buildBatchInsertStatement(build, table, fields, rows[:maxRows])
		return query, values, maxRows, nil
	}
	// 常规批次通常远小于包预算，先直接验证完整候选批次，避免每次都进入二分构建多份 SQL。
	fullQuery, fullValues := buildBatchInsertStatement(build, table, fields, rows[:maxRows])
	if batchStatementFits(build, fullQuery, fullValues, maxBytes) {
		return fullQuery, fullValues, maxRows, nil
	}

	low, high := 1, maxRows-1
	bestRows := 0
	var bestQuery string
	var bestValues []interface{}
	for low <= high {
		middle := low + (high-low)/2
		query, values := buildBatchInsertStatement(build, table, fields, rows[:middle])
		if batchStatementFits(build, query, values, maxBytes) {
			bestRows = middle
			bestQuery = query
			bestValues = values
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	if bestRows == 0 {
		return "", nil, 0, fmt.Errorf("%w: 表 %q 的单行数据超过 %d 字节预算", ErrBatchStatementTooLarge, table, maxBytes)
	}
	return bestQuery, bestValues, bestRows, nil
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
	if limiter, ok := builder.(BatchRowLimiter); ok {
		if maximum := limiter.MaxBatchRows(); maximum > 0 && rows > maximum {
			rows = maximum
		}
	}
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

// maxBatchStatementBytes 读取方言可选的批量语句字节预算。
func maxBatchStatementBytes(build Builder) int {
	if isNilDatabaseDependency(build) {
		return 0
	}
	sizer, ok := build.(BatchStatementSizer)
	if !ok || isNilDatabaseDependency(sizer) {
		return 0
	}
	return sizer.MaxBatchStatementBytes()
}

// batchStatementFits 使用 database/sql 支持的值类型估算参数包大小。
// 该估算只用于提前分批，最终仍由驱动执行层进行严格校验。
func batchStatementFits(build Builder, query string, values []interface{}, maxBytes int) bool {
	if maxBytes <= 0 {
		return true
	}
	estimated, ok := estimateBatchStatementBytes(build.Rebind(query), values)
	return ok && estimated <= int64(maxBytes)
}

func estimateBatchStatementBytes(query string, values []interface{}) (int64, bool) {
	total := int64(len(query)) + batchStatementEnvelopeBytes
	for _, value := range values {
		valueBytes, ok := estimateBatchValueBytes(value)
		if !ok || total > math.MaxInt64-valueBytes-batchStatementParameterOverheadBytes {
			return 0, false
		}
		total += valueBytes + batchStatementParameterOverheadBytes
	}
	return total, true
}

func estimateBatchValueBytes(value interface{}) (int64, bool) {
	converted, err := driver.DefaultParameterConverter.ConvertValue(value)
	if err != nil {
		return 0, false
	}
	switch typed := converted.(type) {
	case nil:
		return 0, true
	case bool:
		return 1, true
	case int64, uint64, float64:
		return 8, true
	case []byte:
		return int64(len(typed)), true
	case string:
		return int64(len(typed)), true
	case time.Time:
		return batchStatementTimeValueBytes, true
	default:
		return 0, false
	}
}

// chunkReadLimit 返回带一行 look-ahead 的读取上限，避免精确整批结束时再发空查询。
func chunkReadLimit(count int) (int, bool) {
	if count >= int(^uint(0)>>1) {
		return count, false
	}
	return count + 1, true
}

// ChunkById 基于主键游标分批遍历，避免大表 OFFSET 深分页的性能衰减与遍历中数据增删导致的漏读/重复。
// 每批按主键升序取 count 条，下一批从上一批最大主键之后继续。回调返回 false 可提前终止。
// pkField 必须是有序、唯一的主键列（默认 "id"）。
func (q *Query) ChunkById(count int, pkField string, callback func(rows []map[string]interface{}) bool) error {
	return q.ChunkByIdWithCodec(count, pkField, OrderedCursorCodec{}, callback)
}

// ChunkByIdWithCodec 使用与数据库列类型及 collation 一致的显式 codec 校验游标推进。
// 文本、二进制、DECIMAL 文本和自定义主键必须使用该入口。
func (q *Query) ChunkByIdWithCodec(count int, pkField string, codec CursorCodec, callback func(rows []map[string]interface{}) bool) error {
	if err := q.ensureValid(); err != nil {
		return q.reportError("chunk_by_id", err, nil)
	}
	if count <= 0 {
		count = 100
	}
	if count > maxQueryResultRows-1 {
		return q.reportError("chunk_by_id", fmt.Errorf("%w: ChunkById 大小超过 %d", ErrInvalidPagination, maxQueryResultRows-1), nil)
	}
	if callback == nil {
		return q.reportError("chunk_by_id", fmt.Errorf("%w: ChunkById 回调不能为空", ErrInvalidQuery), nil)
	}
	if isNilDatabaseDependency(codec) {
		return q.reportError("chunk_by_id", ErrUnsupportedCursorKey, nil)
	}
	if pkField == "" {
		pkField = "id"
	}
	if err := validateIdentifier(pkField); err != nil {
		return q.reportError("chunk_by_id", fmt.Errorf("unsafe pk field: %w", err), nil)
	}

	readLimit := count
	hasLookAhead := q.canStreamSQLRows()
	if hasLookAhead {
		readLimit, hasLookAhead = chunkReadLimit(count)
	}
	var lastID interface{}
	for {
		cloned := q.clone()
		if lastID != nil {
			cloned = cloned.WhereField(pkField, ">", lastID)
		}
		// 主键升序 + LIMIT 实现游标分页（不使用 OFFSET）。
		cloned.order = pkField
		cloned.limit = readLimit
		cloned.offset = 0

		rows, err := cloned.Select()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		hasMore := hasLookAhead && len(rows) > count
		visibleRows := rows
		if hasMore {
			visibleRows = rows[:count]
		}
		next, err := cursorPageLastCursor(visibleRows, pkField, lastID, codec)
		if err != nil {
			return q.reportError("chunk_by_id", err, nil)
		}
		if !callback(visibleRows) {
			break
		}
		if len(visibleRows) < count {
			break
		}
		if hasLookAhead && !hasMore {
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
	if count > maxQueryResultRows-1 {
		return q.reportError("chunk", fmt.Errorf("%w: Chunk 大小超过 %d", ErrInvalidPagination, maxQueryResultRows-1), nil)
	}
	if callback == nil {
		return q.reportError("chunk", fmt.Errorf("%w: Chunk 回调不能为空", ErrInvalidQuery), nil)
	}

	readLimit := count
	hasLookAhead := q.canStreamSQLRows()
	if hasLookAhead {
		readLimit, hasLookAhead = chunkReadLimit(count)
	}
	page := 1
	for {
		cloned := q.clone()
		cloned.limit = readLimit
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

		hasMore := hasLookAhead && len(rows) > count
		visibleRows := rows
		if hasMore {
			visibleRows = rows[:count]
		}
		if !callback(visibleRows) {
			break
		}

		if len(visibleRows) < count {
			break
		}
		if hasLookAhead && !hasMore {
			break
		}
		page++
	}
	return nil
}
