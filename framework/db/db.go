package db

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	sqlSensitiveAssignmentPattern  = regexp.MustCompile(`(?i)\b(password|passwd|token|secret|authorization|api_key|refresh_token)\b\s*=\s*[^,\s)]+`)
	sqlSensitiveComparisonPattern  = regexp.MustCompile(`(?i)\b([a-z0-9_]*(password|passwd|token|secret|authorization|api_key|refresh_token)[a-z0-9_]*)\b\s*=\s*[^,\s)]+`)
	sqlSensitiveAssignmentReplacer = `${1} = [REDACTED]`
)

// Logger 抽象数据库层所需的最小日志能力，避免与具体日志实现形成循环依赖。
type Logger interface {
	ErrorCtx(msg string, ctx map[string]interface{})
}

// DB 数据库连接管理器（全局单例，线程安全）。
// 只负责持有连接和全局配置，不持有任何查询状态。
// 对应 ThinkPHP 8 的 think\DbManager。
type DB struct {
	mu                 sync.RWMutex
	activeLeases       sync.WaitGroup
	closeOnce          sync.Once
	closeErr           error
	closed             bool
	connection         Connection
	prefix             string
	logger             Logger
	autoTimestamp      bool
	createTimeField    string
	updateTimeField    string
	timestampValueType string
}

// NewDB 创建数据库管理器实例。
func NewDB(conn Connection) *DB {
	return &DB{
		connection:         conn,
		prefix:             "",
		autoTimestamp:      false,
		createTimeField:    "create_time",
		updateTimeField:    "update_time",
		timestampValueType: TimestampValueTypeUnix,
	}
}

// Connect 连接数据库（使用驱动注册表）。
func Connect(config Config) (*DB, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.clone()
	connector, err := GetConnector(config.Type)
	if err != nil {
		return nil, err
	}

	conn, err := connector.Connect(config)
	if err != nil {
		return nil, err
	}
	if isNilDatabaseDependency(conn) {
		return nil, ErrDatabaseUnavailable
	}

	database := NewDB(conn)
	database.prefix = config.Prefix
	database.autoTimestamp = config.AutoTimestamp
	if config.CreateTimeField != "" {
		database.createTimeField = config.CreateTimeField
	}
	if config.UpdateTimeField != "" {
		database.updateTimeField = config.UpdateTimeField
	}
	database.timestampValueType = normalizeTimestampValueType(config.TimestampValueType)

	return database, nil
}

// normalizeTimestampValueType 统一收敛自动时间戳的值类型，避免非法配置扩散到执行阶段。
// 对应 ThinkPHP 支持的四种时间戳类型：int(unix)/timestamp/datetime/date。
func normalizeTimestampValueType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case TimestampValueTypeUnix, "int":
		return TimestampValueTypeUnix
	case TimestampValueTypeDateTime:
		return TimestampValueTypeDateTime
	case TimestampValueTypeTimestamp:
		return TimestampValueTypeTimestamp
	case TimestampValueTypeDate:
		return TimestampValueTypeDate
	default:
		return TimestampValueTypeUnix
	}
}

// defaultTimestampValue 根据框架配置生成字段缺失时应补齐的时间值。
// unix → int64 秒级时间戳；datetime/timestamp → "2006-01-02 15:04:05"；date → "2006-01-02"。
func defaultTimestampValue(now time.Time, valueType string) interface{} {
	switch normalizeTimestampValueType(valueType) {
	case TimestampValueTypeDateTime, TimestampValueTypeTimestamp:
		return now.Format(DefaultTimeFormat)
	case TimestampValueTypeDate:
		return now.Format(DefaultDateFormat)
	default:
		return now.Unix()
	}
}

// defaultTimestampString 根据框架配置生成字符串类型字段应写入的时间值。
func defaultTimestampString(now time.Time, valueType string) string {
	switch normalizeTimestampValueType(valueType) {
	case TimestampValueTypeDateTime, TimestampValueTypeTimestamp:
		return now.Format(DefaultTimeFormat)
	case TimestampValueTypeDate:
		return now.Format(DefaultDateFormat)
	default:
		return strconv.FormatInt(now.Unix(), 10)
	}
}

// setAutoTimestamp 按字段当前值类型与全局配置自动补齐时间戳，统一 Query/事务/Model 的时间写入策略。
func setAutoTimestamp(data map[string]interface{}, field string, now time.Time, valueType string) error {
	if data == nil || field == "" {
		return fmt.Errorf("%w: 时间戳字段为空", ErrInvalidDatabaseConfig)
	}
	unix := now.Unix()
	if value, ok := data[field]; ok {
		switch typed := value.(type) {
		case int:
			if typed == 0 {
				if strconv.IntSize == 32 && (unix < -1<<31 || unix > 1<<31-1) {
					return ErrTimestampOverflow
				}
				data[field] = int(unix)
			}
		case int8:
			if typed == 0 {
				if unix < -1<<7 || unix > 1<<7-1 {
					return ErrTimestampOverflow
				}
				data[field] = int8(unix)
			}
		case int16:
			if typed == 0 {
				if unix < -1<<15 || unix > 1<<15-1 {
					return ErrTimestampOverflow
				}
				data[field] = int16(unix)
			}
		case int32:
			if typed == 0 {
				if unix < -1<<31 || unix > 1<<31-1 {
					return ErrTimestampOverflow
				}
				data[field] = int32(unix)
			}
		case int64:
			if typed == 0 {
				data[field] = unix
			}
		case uint:
			if typed == 0 {
				if unix < 0 || strconv.IntSize == 32 && uint64(unix) > 1<<32-1 {
					return ErrTimestampOverflow
				}
				data[field] = uint(unix)
			}
		case uint8:
			if typed == 0 {
				if unix < 0 || unix > 1<<8-1 {
					return ErrTimestampOverflow
				}
				data[field] = uint8(unix)
			}
		case uint16:
			if typed == 0 {
				if unix < 0 || unix > 1<<16-1 {
					return ErrTimestampOverflow
				}
				data[field] = uint16(unix)
			}
		case uint32:
			if typed == 0 {
				if unix < 0 || uint64(unix) > 1<<32-1 {
					return ErrTimestampOverflow
				}
				data[field] = uint32(unix)
			}
		case uint64:
			if typed == 0 {
				if unix < 0 {
					return ErrTimestampOverflow
				}
				data[field] = uint64(unix)
			}
		case string:
			if typed == "" {
				data[field] = defaultTimestampString(now, valueType)
			}
		case time.Time:
			if typed.IsZero() {
				data[field] = now
			}
		case nil:
			data[field] = defaultTimestampValue(now, valueType)
		default:
			reflected := reflect.ValueOf(value)
			if reflected.IsValid() && reflected.IsZero() {
				return fmt.Errorf("%w: 时间戳字段 %q 不支持零值类型 %T", ErrInvalidDatabaseConfig, field, value)
			}
		}
		return nil
	}
	data[field] = defaultTimestampValue(now, valueType)
	return nil
}

// Table 使用完整表名创建查询构建器（不自动拼接前缀）。
// 对应 ThinkPHP 的 Db::table('think_user')，传入的表名必须包含前缀。
func (db *DB) Table(name string) *Query {
	return newQuery(db, name, true)
}

// Name 使用短表名创建查询构建器（自动拼接配置的表前缀）。
// 对应 ThinkPHP 的 Db::name('user')，框架自动追加 prefix 配置。
func (db *DB) Name(name string) *Query {
	return newQuery(db, name)
}

// fullTableName 返回最终落库表名。
func (db *DB) fullTableName(name string) string {
	if db.prefix != "" {
		return db.prefix + name
	}
	return name
}

// ResolveTableName 对外暴露完整表名解析能力，便于原生 SQL 与 ORM 共用前缀规则。
func (db *DB) ResolveTableName(name string) string {
	return db.fullTableName(name)
}

// GetConnection 返回底层连接，供高阶调用使用。
func (db *DB) GetConnection() (Connection, error) {
	return db.connectionSnapshot()
}

// SetLogger 设置数据库错误日志记录器。
func (db *DB) SetLogger(logger Logger) {
	if db == nil {
		return
	}
	db.mu.Lock()
	db.logger = logger
	db.mu.Unlock()
}

// Close 关闭数据库连接。
func (db *DB) Close() error {
	if db == nil {
		return nil
	}
	db.closeOnce.Do(func() {
		db.mu.Lock()
		db.closed = true
		connection := db.connection
		db.mu.Unlock()
		// 状态锁只负责阻止新租约；等待期间必须允许错误日志和配置快照读取，
		// 否则在途操作报错时会与 Close 形成递归读锁死锁。
		db.activeLeases.Wait()
		if !isNilDatabaseDependency(connection) {
			db.closeErr = connection.Close()
		}
	})
	return db.closeErr
}

// Query 执行原生 SQL 查询。
// 对应 ThinkPHP 的 Db::query()。
func (db *DB) Query(sql string, args ...interface{}) ([]map[string]interface{}, error) {
	return db.QueryContext(context.Background(), sql, args...)
}

// QueryContext 使用显式上下文执行原生查询。
func (db *DB) QueryContext(ctx context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error) {
	if ctx == nil {
		return nil, db.reportError("query", fmt.Errorf("%w: 查询上下文不能为空", ErrInvalidQuery), nil)
	}
	if err := validateRawStatement(sql, len(args)); err != nil {
		return nil, db.reportError("query", err, nil)
	}
	connection, release, stateErr := db.acquireConnection()
	if stateErr != nil {
		return nil, db.reportError("query", stateErr, nil)
	}
	defer release()
	if raw, ok := connection.(ContextualRawQueryable); ok {
		rows, err := raw.QueryContext(ctx, sql, args...)
		if err != nil {
			return nil, db.reportError("query", err, map[string]interface{}{
				"sql": redactSQLText(sql), "args": redactArgCount(args),
			})
		}
		return rows, nil
	}
	if raw, ok := connection.(RawQueryable); ok {
		rows, err := raw.Query(sql, args...)
		if err != nil {
			return nil, db.reportError("query", err, map[string]interface{}{
				"sql":  redactSQLText(sql),
				"args": redactArgCount(args),
			})
		}
		return rows, nil
	}

	return nil, db.reportError("query", fmt.Errorf("current connection does not support raw queries"), map[string]interface{}{
		"sql":  redactSQLText(sql),
		"args": redactArgCount(args),
	})
}

// Execute 执行原生 SQL 命令。
// 对应 ThinkPHP 的 Db::execute()。
func (db *DB) Execute(sql string, args ...interface{}) (int64, error) {
	return db.ExecuteContext(context.Background(), sql, args...)
}

// ExecuteContext 使用显式上下文执行原生命令。
func (db *DB) ExecuteContext(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	if ctx == nil {
		return 0, db.reportError("execute", fmt.Errorf("%w: 执行上下文不能为空", ErrInvalidQuery), nil)
	}
	if err := validateRawStatement(sql, len(args)); err != nil {
		return 0, db.reportError("execute", err, nil)
	}
	connection, release, stateErr := db.acquireConnection()
	if stateErr != nil {
		return 0, db.reportError("execute", stateErr, nil)
	}
	defer release()
	if raw, ok := connection.(ContextualRawQueryable); ok {
		affected, err := raw.ExecuteContext(ctx, sql, args...)
		if err != nil {
			return 0, db.reportError("execute", err, map[string]interface{}{
				"sql": redactSQLText(sql), "args": redactArgCount(args),
			})
		}
		return affected, nil
	}
	if raw, ok := connection.(RawQueryable); ok {
		affected, err := raw.Execute(sql, args...)
		if err != nil {
			return 0, db.reportError("execute", err, map[string]interface{}{
				"sql":  redactSQLText(sql),
				"args": redactArgCount(args),
			})
		}
		return affected, nil
	}

	return 0, db.reportError("execute", fmt.Errorf("current connection does not support raw execution"), map[string]interface{}{
		"sql":  redactSQLText(sql),
		"args": redactArgCount(args),
	})
}

// redactDataKeys 仅保留写入数据的字段名用于排障，绝不记录字段值，
// 避免密码、令牌、个人信息等通过错误日志泄露。
func redactDataKeys(data map[string]interface{}) map[string]interface{} {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return map[string]interface{}{"fields": keys}
}

// redactArgCount 把参数列表降级为数量描述，避免把绑定值（可能含敏感数据）写入日志。
func redactArgCount(args []interface{}) string {
	return fmt.Sprintf("%d args (redacted)", len(args))
}

func redactSQLText(sqlText string) string {
	if sqlText == "" {
		return ""
	}
	redacted := redactSQLLiteralsAndComments(sqlText)
	redacted = sqlSensitiveAssignmentPattern.ReplaceAllString(redacted, sqlSensitiveAssignmentReplacer)
	redacted = sqlSensitiveComparisonPattern.ReplaceAllString(redacted, sqlSensitiveAssignmentReplacer)
	return redacted
}

// reportError 统一记录数据库错误并保留原始错误返回给上层调用方。
func (db *DB) reportError(operation string, err error, ctx map[string]interface{}) error {
	if err == nil {
		return nil
	}

	var logger Logger
	if db != nil {
		db.mu.RLock()
		logger = db.logger
		db.mu.RUnlock()
	}
	if logger != nil {
		logCtx := map[string]interface{}{
			"component": "db",
			"operation": operation,
		}
		for key, value := range ctx {
			logCtx[key] = value
		}
		logger.ErrorCtx(fmt.Sprintf("database %s failed: %v", operation, err), logCtx)
	}

	return err
}

func (db *DB) connectionSnapshot() (Connection, error) {
	connection, release, err := db.acquireConnection()
	if release != nil {
		release()
	}
	return connection, err
}

// acquireConnection 为一次数据库操作登记独立生命周期租约，
// 状态读锁仅覆盖校验和计数登记，Close 会等待全部租约释放后再关闭底层连接。
func (db *DB) acquireConnection() (Connection, func(), error) {
	if db == nil {
		return nil, nil, ErrDatabaseUnavailable
	}
	db.mu.RLock()
	if db.closed {
		db.mu.RUnlock()
		return nil, nil, ErrDatabaseClosed
	}
	if isNilDatabaseDependency(db.connection) {
		db.mu.RUnlock()
		return nil, nil, ErrDatabaseUnavailable
	}
	connection := db.connection
	db.activeLeases.Add(1)
	db.mu.RUnlock()

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(db.activeLeases.Done)
	}
	return connection, release, nil
}
