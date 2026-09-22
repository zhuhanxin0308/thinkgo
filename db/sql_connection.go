package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

const defaultStatementCacheCapacity = 128

// SQLConnection 封装 database/sql 与方言构建器。
type SQLConnection struct {
	DB                *sql.DB
	Builder           Builder
	identity          ConnectionID
	identityOnce      sync.Once
	locationMu        sync.RWMutex
	location          *time.Location
	statementMu       sync.RWMutex
	statements        map[string]*cachedSQLStatement
	statementOrder    []string
	statementCapacity int
	statementsClosed  bool
	statementUsers    sync.WaitGroup
	statementShutdown sync.Once
	statementCloseErr error
	schemaMu          sync.RWMutex
	schemaCache       map[string]TableSchema
}

// NewSQLConnection 创建由框架管理的 SQL 连接，并启用有界语句缓存。
// 手工构造 SQLConnection 时保持零值行为，不强制底层驱动支持 Prepare。
func NewSQLConnection(database *sql.DB, build Builder) *SQLConnection {
	return &SQLConnection{
		DB:                database,
		Builder:           build,
		statementCapacity: defaultStatementCacheCapacity,
		statementOrder:    make([]string, 0, defaultStatementCacheCapacity),
		statements:        make(map[string]*cachedSQLStatement, defaultStatementCacheCapacity),
	}
}

// SetLocation 设置 SQL 查询结果使用的应用时区。
func (c *SQLConnection) SetLocation(location *time.Location) {
	if c == nil {
		return
	}
	if location == nil {
		location = time.UTC
	}
	c.locationMu.Lock()
	c.location = location
	c.locationMu.Unlock()
}

// Location 返回 SQL 连接结果使用的应用时区。
func (c *SQLConnection) Location() *time.Location {
	if c == nil {
		return time.UTC
	}
	c.locationMu.RLock()
	location := c.location
	c.locationMu.RUnlock()
	if location == nil {
		return time.UTC
	}
	return location
}

func (c *SQLConnection) ConnectionID() ConnectionID {
	if c == nil {
		return ""
	}
	c.identityOnce.Do(func() { c.identity = NewConnectionID("sql") })
	return c.identity
}

var (
	_ Connection              = (*SQLConnection)(nil)
	_ LocationAwareConnection = (*SQLConnection)(nil)
	_ CapabilityProvider      = (*SQLConnection)(nil)
	_ RawQueryable            = (*SQLConnection)(nil)
	_ ContextualRawQueryable  = (*SQLConnection)(nil)
)

// Capabilities describes the identifier shapes produced by each SQL path.
// LastInsertId and Oracle RETURNING are integer-only; PostgreSQL and SQL Server
// scan returned primary keys through database/sql and may yield strings too.
func (c *SQLConnection) Capabilities() DriverCapabilities {
	kinds := []InsertIDKind{InsertIDInteger}
	if c != nil && c.Builder != nil {
		switch c.Builder.DialectName() {
		case "postgres", "sqlserver":
			kinds = []InsertIDKind{InsertIDInteger, InsertIDString, InsertIDDynamic}
		}
	}
	return DriverCapabilities{InsertIDKinds: kinds}
}

func (c *SQLConnection) Select(ctx context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	rows, err := c.selectRowsContext(ctx, request)
	if err != nil {
		return nil, err
	}
	return scanSQLRows(rows, c.Builder, c.Location())
}

// SelectEach 按行消费结构化查询结果，避免一次性物化整个结果集。
// 回调返回 false 时立即停止读取，但仍会关闭底层结果集并返回关闭错误。
func (c *SQLConnection) SelectEach(ctx context.Context, request SelectRequest, callback func(map[string]interface{}) bool) error {
	if callback == nil {
		return fmt.Errorf("%w: 流式查询回调不能为空", ErrInvalidQuery)
	}
	rows, err := c.selectRowsContext(ctx, request)
	if err != nil {
		return err
	}
	return scanSQLRowsEach(rows, c.Builder, c.Location(), callback)
}

// selectColumn 执行只需要目标列的结构化查询，避免为 Column 结果创建完整行 map。
func (c *SQLConnection) selectColumn(ctx context.Context, request SelectRequest, field, key string) (interface{}, error) {
	rows, err := c.selectRowsContext(ctx, request)
	if err != nil {
		return nil, err
	}
	naiveTimeAsWallClock := c.Builder != nil && strings.EqualFold(strings.TrimSpace(c.Builder.DialectName()), "postgres")
	return scanSQLColumnRows(rows, c.Location(), naiveTimeAsWallClock, field, key)
}

// selectRowsContext 统一构造结构化 SELECT 的结果集，供物化和流式扫描共享校验与语句缓存路径。
func (c *SQLConnection) selectRowsContext(ctx context.Context, request SelectRequest) (*sql.Rows, error) {
	if err := c.validateContext(ctx); err != nil {
		return nil, err
	}
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return nil, err
	}
	fields := request.Fields()
	if fields == "*" {
		if cached := c.cachedSchemaFields(request.Table()); cached != "" {
			fields = cached
		}
	}
	fieldsForValidation := fields
	if aggregate := request.Aggregate(); aggregate != nil {
		if err := aggregate.validate(); err != nil {
			return nil, err
		}
		fields = fmt.Sprintf("%s(%s) AS %s", strings.ToUpper(strings.TrimSpace(aggregate.Function)), aggregate.Field, aggregate.Alias)
		fieldsForValidation = "*"
	}
	if err := validateSelectArguments(request.Table(), fieldsForValidation, where, args, request.Order(), request.Limit(), request.Offset()); err != nil {
		return nil, err
	}
	if err := validateBindParameterBudget(c.Builder, len(args)); err != nil {
		return nil, err
	}
	query := c.Builder.Rebind(c.Builder.Select(request.Table(), fields, where, request.Order(), request.Limit(), request.Offset()))
	return c.queryRowsContext(ctx, query, args...)
}

func (c *SQLConnection) Insert(ctx context.Context, request InsertRequest) (InsertResult, error) {
	if err := c.validateContext(ctx); err != nil {
		return InsertResult{}, err
	}
	data := normalizeDatabaseWriteMap(request.Data(), c.Location())
	if err := validateWriteArguments(request.Table(), data); err != nil {
		return InsertResult{}, err
	}
	if err := validateBindParameterBudget(c.Builder, len(data)); err != nil {
		return InsertResult{}, err
	}
	if request.WantsID() {
		if err := validateIdentifier(request.PrimaryKey()); err != nil {
			return InsertResult{}, fmt.Errorf("%w: 非法回传主键: %w", ErrInvalidQuery, err)
		}
		if !c.Builder.SupportsLastInsertId() {
			return c.insertReturningOperation(ctx, request, data)
		}
	}

	query, values := c.Builder.Insert(request.Table(), data)
	result, err := c.execContext(ctx, c.Builder.Rebind(query), values...)
	if err != nil {
		return InsertResult{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return InsertResult{}, err
	}
	operationResult := InsertResult{Affected: affected, Data: data}
	if request.WantsID() {
		operationResult.ID, err = result.LastInsertId()
		operationResult.IDKnown = err == nil
		if err != nil {
			return InsertResult{}, err
		}
	}
	return operationResult, operationResult.Validate()
}

func (c *SQLConnection) insertReturningOperation(ctx context.Context, request InsertRequest, data map[string]interface{}) (InsertResult, error) {
	query, values, ok := c.Builder.InsertReturning(request.Table(), data, request.PrimaryKey())
	if !ok {
		return InsertResult{}, ErrInsertIDUnavailable
	}
	operationResult := InsertResult{Affected: 1, Data: data}
	if len(values) > 0 {
		if output, isOutput := values[len(values)-1].(sql.Out); isOutput {
			if _, err := c.execContext(ctx, c.Builder.Rebind(query), values...); err != nil {
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
	row, err := c.queryRowContext(ctx, c.Builder.Rebind(query), values...)
	if err != nil {
		return InsertResult{}, err
	}
	if err := row.Scan(&id); err != nil {
		return InsertResult{}, err
	}
	operationResult.ID, operationResult.IDKnown = id, true
	return operationResult, operationResult.Validate()
}

func sqlOutputValue(destination interface{}) (interface{}, error) {
	value := reflect.ValueOf(destination)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return nil, fmt.Errorf("%w: RETURNING 输出目标必须是非空指针", ErrInvalidDatabaseRow)
	}
	return value.Elem().Interface(), nil
}

func (c *SQLConnection) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	if err := c.validateContext(ctx); err != nil {
		return UpdateResult{}, err
	}
	data := normalizeDatabaseWriteMap(request.Data(), c.Location())
	if err := validateWriteArguments(request.Table(), data); err != nil {
		return UpdateResult{}, err
	}
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return UpdateResult{}, err
	}
	if err := validateMutationPredicate(where, args); err != nil {
		return UpdateResult{}, err
	}
	if err := validateBindParameterBudget(c.Builder, len(data), len(args)); err != nil {
		return UpdateResult{}, err
	}
	query, values := c.Builder.Update(request.Table(), data, where)
	result, err := c.execContext(ctx, c.Builder.Rebind(query), append(values, args...)...)
	if err != nil {
		return UpdateResult{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return UpdateResult{}, err
	}
	operationResult := UpdateResult{Affected: affected, Data: data}
	return operationResult, operationResult.Validate()
}

func (c *SQLConnection) Delete(ctx context.Context, request DeleteRequest) (DeleteResult, error) {
	if err := c.validateContext(ctx); err != nil {
		return DeleteResult{}, err
	}
	if err := validateIdentifier(request.Table()); err != nil {
		return DeleteResult{}, fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return DeleteResult{}, err
	}
	if err := validateMutationPredicate(where, args); err != nil {
		return DeleteResult{}, err
	}
	if err := validateBindParameterBudget(c.Builder, len(args)); err != nil {
		return DeleteResult{}, err
	}
	result, err := c.execContext(ctx, c.Builder.Rebind(c.Builder.Delete(request.Table(), where)), args...)
	if err != nil {
		return DeleteResult{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return DeleteResult{}, err
	}
	operationResult := DeleteResult{Deleted: affected}
	return operationResult, operationResult.Validate()
}

func (c *SQLConnection) Count(ctx context.Context, request CountRequest) (int64, error) {
	if err := c.validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateIdentifier(request.Table()); err != nil {
		return 0, fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	where, args, err := request.Predicate().compileSQL()
	if err != nil {
		return 0, err
	}
	if err := validateBindParameterBudget(c.Builder, len(args)); err != nil {
		return 0, err
	}
	var count int64
	row, err := c.queryRowContext(ctx, c.Builder.Rebind(c.Builder.Count(request.Table(), where)), args...)
	if err != nil {
		return 0, err
	}
	err = row.Scan(&count)
	return count, err
}

func (c *SQLConnection) Close() error {
	if c == nil || c.DB == nil {
		return ErrDatabaseUnavailable
	}
	return errors.Join(c.closeStatements(), c.DB.Close())
}

func (c *SQLConnection) Query(rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	return c.QueryContext(context.Background(), rawSQL, args...)
}

func (c *SQLConnection) QueryContext(ctx context.Context, rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	if err := c.validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return nil, err
	}
	if err := validateBindParameterBudget(c.Builder, len(args)); err != nil {
		return nil, err
	}
	rows, err := c.queryRowsContext(ctx, c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return nil, err
	}
	return scanSQLRows(rows, c.Builder, c.Location())
}

// QueryEachContext 按行消费原生 SQL 结果，复用上下文、参数预算和语句缓存。
func (c *SQLConnection) QueryEachContext(ctx context.Context, rawSQL string, callback func(map[string]interface{}) bool, args ...interface{}) error {
	if callback == nil {
		return fmt.Errorf("%w: 流式查询回调不能为空", ErrInvalidQuery)
	}
	if err := c.validateContext(ctx); err != nil {
		return err
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return err
	}
	if err := validateBindParameterBudget(c.Builder, len(args)); err != nil {
		return err
	}
	rows, err := c.queryRowsContext(ctx, c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return err
	}
	return scanSQLRowsEach(rows, c.Builder, c.Location(), callback)
}

func (c *SQLConnection) Execute(rawSQL string, args ...interface{}) (int64, error) {
	return c.ExecuteContext(context.Background(), rawSQL, args...)
}

func (c *SQLConnection) ExecuteContext(ctx context.Context, rawSQL string, args ...interface{}) (int64, error) {
	if err := c.validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return 0, err
	}
	if err := validateBindParameterBudget(c.Builder, len(args)); err != nil {
		return 0, err
	}
	result, err := c.execContext(ctx, c.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// queryRowsContext 优先复用连接级语句，缓存未启用时退回 database/sql 原生路径。
func (c *SQLConnection) queryRowsContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if c.statementCapacity <= 0 {
		return c.DB.QueryContext(ctx, query, args...)
	}
	entry, err := c.acquireStatement(ctx, query)
	if err != nil {
		return nil, err
	}
	defer c.releaseStatement(entry)
	// database/sql 在返回 Rows 时接管底层语句引用，缓存租约只需覆盖开始执行阶段。
	return entry.statement.QueryContext(ctx, args...)
}

// queryRowContext 执行单行查询，并与批量行查询共享同一语句缓存。
func (c *SQLConnection) queryRowContext(ctx context.Context, query string, args ...interface{}) (*sql.Row, error) {
	// QueryRow 的实际执行发生在后续 Scan，不能在此处持有缓存锁，因此使用 database/sql 原生路径。
	return c.DB.QueryRowContext(ctx, query, args...), nil
}

// execContext 执行写操作，并复用连接级语句。
func (c *SQLConnection) execContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if c.statementCapacity <= 0 {
		return c.DB.ExecContext(ctx, query, args...)
	}
	entry, err := c.acquireStatement(ctx, query)
	if err != nil {
		return nil, err
	}
	defer c.releaseStatement(entry)
	return entry.statement.ExecContext(ctx, args...)
}

func (c *SQLConnection) validateContext(ctx context.Context) error {
	if c == nil || c.DB == nil || isNilDatabaseDependency(c.Builder) {
		return ErrDatabaseUnavailable
	}
	if ctx == nil {
		return fmt.Errorf("%w: SQL 上下文不能为空", ErrInvalidQuery)
	}
	return nil
}

func validateSelectArguments(table, fields string, where []string, args []interface{}, order string, limit, offset int) error {
	if err := validateIdentifier(table); err != nil {
		return fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	if err := validateIdentifierList(fields); err != nil {
		return fmt.Errorf("%w: 非法字段列表: %w", ErrInvalidQuery, err)
	}
	if err := validateOrderClause(order); err != nil {
		return fmt.Errorf("%w: 非法排序: %w", ErrInvalidQuery, err)
	}
	if limit < 0 || offset < 0 {
		return fmt.Errorf("%w: limit 或 offset 不能为负数", ErrInvalidPagination)
	}
	if err := validatePlaceholderCount(strings.Join(where, " AND "), len(args)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

func validateWriteArguments(table string, data map[string]interface{}) error {
	if err := validateIdentifier(table); err != nil {
		return fmt.Errorf("%w: 非法表名: %w", ErrInvalidQuery, err)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: 写入数据不能为空", ErrInvalidQuery)
	}
	if err := validateDataKeys(data); err != nil {
		return fmt.Errorf("%w: 非法写入字段: %w", ErrInvalidQuery, err)
	}
	return nil
}

func validateMutationPredicate(where []string, args []interface{}) error {
	if len(where) == 0 {
		return ErrUnsafeFullTableMutation
	}
	if err := validatePlaceholderCount(strings.Join(where, " AND "), len(args)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

// scanSQLRows 按 SQL 方言处理结果中的无时区时间，避免 PostgreSQL 墙上时间被错误平移。
func scanSQLRows(rows *sql.Rows, builder Builder, location *time.Location) (results []map[string]interface{}, resultErr error) {
	naiveTimeAsWallClock := builder != nil && strings.EqualFold(strings.TrimSpace(builder.DialectName()), "postgres")
	return scanRowsWithPolicy(rows, location, naiveTimeAsWallClock)
}

func scanSQLRowsEach(rows *sql.Rows, builder Builder, location *time.Location, callback func(map[string]interface{}) bool) error {
	if callback == nil {
		return fmt.Errorf("%w: 流式查询回调不能为空", ErrInvalidQuery)
	}
	naiveTimeAsWallClock := builder != nil && strings.EqualFold(strings.TrimSpace(builder.DialectName()), "postgres")
	return walkSQLRows(rows, location, naiveTimeAsWallClock, callback)
}

func scanRowsWithPolicy(rows *sql.Rows, location *time.Location, naiveTimeAsWallClock bool) (results []map[string]interface{}, resultErr error) {
	results = make([]map[string]interface{}, 0)
	tooMany := false
	resultErr = walkSQLRows(rows, location, naiveTimeAsWallClock, func(entry map[string]interface{}) bool {
		if len(results) >= maxQueryResultRows {
			tooMany = true
			return false
		}
		results = append(results, entry)
		return true
	})
	if tooMany {
		resultErr = errors.Join(resultErr, fmt.Errorf("%w: 结果行数超过 %d", ErrQueryResultTooMany, maxQueryResultRows))
	}
	return results, resultErr
}

// walkSQLRows 共享结构化行扫描策略，物化查询和流式查询必须保持相同的类型转换规则。
func walkSQLRows(rows *sql.Rows, location *time.Location, naiveTimeAsWallClock bool, callback func(map[string]interface{}) bool) (resultErr error) {
	if rows == nil {
		return fmt.Errorf("%w: 结果集不能为空", ErrInvalidDatabaseRow)
	}
	defer func() {
		resultErr = errors.Join(resultErr, rows.Close())
	}()

	if location == nil {
		location = time.UTC
	}
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	columnTypes, _ := rows.ColumnTypes()
	seenColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, duplicated := seenColumns[column]; duplicated {
			return fmt.Errorf("%w: 结果包含重复列 %q，请使用唯一别名", ErrInvalidDatabaseRow, column)
		}
		seenColumns[column] = struct{}{}
	}

	values := make([]interface{}, len(columns))
	scanArgs := make([]interface{}, len(columns))
	for index := range values {
		scanArgs[index] = &values[index]
	}
	for rows.Next() {
		for index := range values {
			values[index] = nil
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return err
		}
		entry := make(map[string]interface{}, len(columns))
		for index, column := range columns {
			databaseType := ""
			if index < len(columnTypes) && columnTypes[index] != nil {
				databaseType = columnTypes[index].DatabaseTypeName()
			}
			entry[column] = cloneScannedValueWithDatabaseType(values[index], location, naiveTimeAsWallClock, databaseType)
		}
		if !callback(entry) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// scanSQLColumnRows 只扫描 Column 所需的列，避免大结果集为每行创建完整 map。
func scanSQLColumnRows(rows *sql.Rows, location *time.Location, naiveTimeAsWallClock bool, field, key string) (result interface{}, resultErr error) {
	if rows == nil {
		return nil, fmt.Errorf("%w: 结果集不能为 nil", ErrInvalidDatabaseRow)
	}
	defer func() {
		resultErr = errors.Join(resultErr, rows.Close())
	}()
	if location == nil {
		location = time.UTC
	}
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	columnTypes, _ := rows.ColumnTypes()
	seenColumns := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, duplicated := seenColumns[column]; duplicated {
			return nil, fmt.Errorf("%w: 结果包含重复列 %q，请使用唯一别名", ErrInvalidDatabaseRow, column)
		}
		seenColumns[column] = struct{}{}
	}
	fieldIndex, fieldFound := findSQLResultColumnIndex(columns, field)
	if !fieldFound {
		return nil, fmt.Errorf("%w: 结果缺少字段 %q", ErrInvalidDatabaseRow, field)
	}
	keyIndex := -1
	if key != "" {
		var keyFound bool
		keyIndex, keyFound = findSQLResultColumnIndex(columns, key)
		if !keyFound {
			return nil, fmt.Errorf("%w: 结果缺少 key 字段 %q", ErrInvalidDatabaseRow, key)
		}
	}

	values := make([]interface{}, len(columns))
	scanArgs := make([]interface{}, len(columns))
	for index := range values {
		scanArgs[index] = &values[index]
	}
	if keyIndex >= 0 {
		resultMap := make(map[string]interface{})
		rowIndex := 0
		for rows.Next() {
			if rowIndex >= maxQueryResultRows {
				return nil, fmt.Errorf("%w: 结果行数超过 %d", ErrQueryResultTooMany, maxQueryResultRows)
			}
			for index := range values {
				values[index] = nil
			}
			if err := rows.Scan(scanArgs...); err != nil {
				return nil, err
			}
			fieldValue := cloneScannedColumnValue(values[fieldIndex], columnTypes, fieldIndex, location, naiveTimeAsWallClock)
			keyValue := cloneScannedColumnValue(values[keyIndex], columnTypes, keyIndex, location, naiveTimeAsWallClock)
			if keyValue == nil {
				return nil, fmt.Errorf("%w: 第 %d 行缺少 key 或目标字段", ErrInvalidDatabaseRow, rowIndex)
			}
			keyText := fmt.Sprint(keyValue)
			if _, duplicated := resultMap[keyText]; duplicated {
				return nil, fmt.Errorf("%w: key %q 重复或字符串化后冲突", ErrInvalidDatabaseRow, keyText)
			}
			resultMap[keyText] = fieldValue
			rowIndex++
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return resultMap, nil
	}

	resultList := make([]interface{}, 0)
	for rows.Next() {
		if len(resultList) >= maxQueryResultRows {
			return nil, fmt.Errorf("%w: 结果行数超过 %d", ErrQueryResultTooMany, maxQueryResultRows)
		}
		for index := range values {
			values[index] = nil
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}
		resultList = append(resultList, cloneScannedColumnValue(values[fieldIndex], columnTypes, fieldIndex, location, naiveTimeAsWallClock))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return resultList, nil
}

// cloneScannedColumnValue 复用统一的时间和二进制转换规则，并只读取当前列的元数据。
func cloneScannedColumnValue(value interface{}, columnTypes []*sql.ColumnType, index int, location *time.Location, naiveTimeAsWallClock bool) interface{} {
	databaseType := ""
	if index < len(columnTypes) && columnTypes[index] != nil {
		databaseType = columnTypes[index].DatabaseTypeName()
	}
	return cloneScannedValueWithDatabaseType(value, location, naiveTimeAsWallClock, databaseType)
}

// findSQLResultColumnIndex 兼容限定字段在驱动结果中返回完整名或末段列名的差异。
func findSQLResultColumnIndex(columns []string, field string) (int, bool) {
	for index, column := range columns {
		if column == field {
			return index, true
		}
	}
	separator := strings.LastIndexByte(field, '.')
	if separator < 0 || separator == len(field)-1 {
		return -1, false
	}
	shortField := field[separator+1:]
	for index, column := range columns {
		if column == shortField {
			return index, true
		}
	}
	return -1, false
}

func cloneScannedValue(value interface{}, location *time.Location) interface{} {
	return cloneScannedValueWithDatabaseType(value, location, false, "")
}

// cloneScannedValueWithDatabaseType 按数据库列类型转换时间结果，保留无时区字段的墙上时间。
func cloneScannedValueWithDatabaseType(value interface{}, location *time.Location, naiveTimeAsWallClock bool, databaseType string) interface{} {
	if location == nil {
		location = time.UTC
	}
	if timestamp, ok := value.(time.Time); ok {
		if naiveTimeAsWallClock && isPostgresWallClockType(databaseType) {
			return time.Date(
				timestamp.Year(), timestamp.Month(), timestamp.Day(),
				timestamp.Hour(), timestamp.Minute(), timestamp.Second(), timestamp.Nanosecond(), location,
			)
		}
		return timestamp.In(location)
	}
	if binary, ok := value.([]byte); ok {
		return append([]byte(nil), binary...)
	}
	return value
}

func isPostgresWallClockType(databaseType string) bool {
	switch strings.ToUpper(strings.TrimSpace(databaseType)) {
	case "DATE", "TIME", "TIME WITHOUT TIME ZONE", "TIMESTAMP", "TIMESTAMP WITHOUT TIME ZONE":
		return true
	default:
		return false
	}
}
