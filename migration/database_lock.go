package migration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

const (
	migrationLockNamespace     = "thinkgo:migrations:v2"
	maximumOracleLockTimeout   = 32767 * time.Second
	maximumSQLServerLockMillis = int64(math.MaxInt32)
	oracleExclusiveLockMode    = int64(6)
	sqliteBusyResultCode       = int64(5)
	sqliteLockedResultCode     = int64(6)
	sqliteLockRetryInitial     = 2 * time.Millisecond
	sqliteLockRetryMaximum     = 25 * time.Millisecond
)

type dialectMigrationLocker interface {
	Acquire(context.Context, time.Duration) error
	Release(context.Context, bool) error
}

func newDialectMigrationLocker(session db.PinnedSQLConnection) (dialectMigrationLocker, error) {
	if session == nil {
		return nil, fmt.Errorf("%w: pinned SQL 会话为空", ErrInvalidMigration)
	}
	switch session.DialectName() {
	case "mysql":
		return &mysqlMigrationLocker{session: session}, nil
	case "postgres":
		first, second := migrationLockKeyPair(migrationLockNamespace)
		return &postgresMigrationLocker{session: session, firstKey: first, secondKey: second}, nil
	case "sqlite":
		return &sqliteMigrationLocker{session: session}, nil
	case "sqlserver":
		return &sqlServerMigrationLocker{session: session}, nil
	case "oracle":
		return &oracleMigrationLocker{session: session}, nil
	default:
		return nil, fmt.Errorf("%w: 不支持迁移锁方言 %q", ErrInvalidMigration, session.DialectName())
	}
}

type mysqlMigrationLocker struct {
	session  db.PinnedSQLConnection
	lockName string
}

func (locker *mysqlMigrationLocker) Acquire(ctx context.Context, timeout time.Duration) error {
	rows, err := locker.session.QueryContext(ctx, "SELECT DATABASE() AS lock_scope")
	if err != nil {
		return fmt.Errorf("%w: MySQL 读取锁作用域失败: %w", ErrMigrationLockUnavailable, err)
	}
	scope, err := migrationLockText(rows, "lock_scope")
	if err != nil {
		return fmt.Errorf("%w: MySQL 当前数据库为空", ErrMigrationLockUnavailable)
	}
	locker.lockName = migrationMySQLLockName(scope)
	rows, err = locker.session.QueryContext(ctx, "SELECT GET_LOCK(?, ?) AS lock_result", locker.lockName, durationCeilingSeconds(timeout))
	if err != nil {
		return fmt.Errorf("%w: MySQL GET_LOCK 执行失败: %w", ErrMigrationLockUnavailable, err)
	}
	result, err := migrationLockInt(rows, "lock_result")
	if err != nil || result != 1 {
		return fmt.Errorf("%w: MySQL GET_LOCK 返回 %d", ErrMigrationLockUnavailable, result)
	}
	return nil
}

func (locker *mysqlMigrationLocker) Release(ctx context.Context, _ bool) error {
	rows, err := locker.session.QueryContext(ctx, "SELECT RELEASE_LOCK(?) AS lock_result", locker.lockName)
	if err != nil {
		return fmt.Errorf("%w: MySQL RELEASE_LOCK 执行失败: %w", ErrMigrationLockLost, err)
	}
	result, parseErr := migrationLockInt(rows, "lock_result")
	if parseErr != nil || result != 1 {
		return fmt.Errorf("%w: MySQL RELEASE_LOCK 返回 %d", ErrMigrationLockLost, result)
	}
	return nil
}

type postgresMigrationLocker struct {
	session   db.PinnedSQLConnection
	firstKey  int32
	secondKey int32
}

func (locker *postgresMigrationLocker) Acquire(ctx context.Context, _ time.Duration) error {
	_, err := locker.session.QueryContext(ctx, "SELECT pg_advisory_lock(?, ?) AS lock_result", locker.firstKey, locker.secondKey)
	if err != nil {
		return fmt.Errorf("%w: PostgreSQL pg_advisory_lock 执行失败: %w", ErrMigrationLockUnavailable, err)
	}
	return nil
}

func (locker *postgresMigrationLocker) Release(ctx context.Context, _ bool) error {
	rows, err := locker.session.QueryContext(ctx, "SELECT pg_advisory_unlock(?, ?) AS lock_result", locker.firstKey, locker.secondKey)
	if err != nil {
		return fmt.Errorf("%w: PostgreSQL pg_advisory_unlock 执行失败: %w", ErrMigrationLockLost, err)
	}
	released, parseErr := migrationLockBool(rows, "lock_result")
	if parseErr != nil || !released {
		return fmt.Errorf("%w: PostgreSQL pg_advisory_unlock 未释放当前锁", ErrMigrationLockLost)
	}
	return nil
}

type sqlServerMigrationLocker struct {
	session db.PinnedSQLConnection
}

func (locker *sqlServerMigrationLocker) Acquire(ctx context.Context, timeout time.Duration) error {
	statement := "DECLARE @result int; EXEC @result = sys.sp_getapplock @Resource = ?, @LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = ?; SELECT @result AS lock_result"
	rows, err := locker.session.QueryContext(ctx, statement, migrationLockNamespace, durationCeilingMilliseconds(timeout))
	if err != nil {
		return fmt.Errorf("%w: SQL Server sp_getapplock 执行失败: %w", ErrMigrationLockUnavailable, err)
	}
	result, parseErr := migrationLockInt(rows, "lock_result")
	if parseErr != nil || result < 0 {
		return fmt.Errorf("%w: SQL Server sp_getapplock 返回 %d", ErrMigrationLockUnavailable, result)
	}
	return nil
}

func (locker *sqlServerMigrationLocker) Release(ctx context.Context, _ bool) error {
	statement := "DECLARE @result int; EXEC @result = sys.sp_releaseapplock @Resource = ?, @LockOwner = 'Session'; SELECT @result AS lock_result"
	rows, err := locker.session.QueryContext(ctx, statement, migrationLockNamespace)
	if err != nil {
		return fmt.Errorf("%w: SQL Server sp_releaseapplock 执行失败: %w", ErrMigrationLockLost, err)
	}
	result, parseErr := migrationLockInt(rows, "lock_result")
	if parseErr != nil || result < 0 {
		return fmt.Errorf("%w: SQL Server sp_releaseapplock 返回 %d", ErrMigrationLockLost, result)
	}
	return nil
}

type oracleMigrationLocker struct {
	session db.PinnedSQLConnection
	lockID  int64
}

func (locker *oracleMigrationLocker) Acquire(ctx context.Context, timeout time.Duration) error {
	scopeSQL := "SELECT SYS_CONTEXT('USERENV', 'DB_UNIQUE_NAME') || ':' || SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') AS lock_scope FROM dual"
	rows, err := locker.session.QueryContext(ctx, scopeSQL)
	if err != nil {
		return fmt.Errorf("%w: Oracle 读取锁作用域失败: %w", ErrMigrationLockUnavailable, err)
	}
	scope, err := migrationLockText(rows, "lock_scope")
	if err != nil {
		return fmt.Errorf("%w: Oracle 数据库锁作用域为空", ErrMigrationLockUnavailable)
	}
	locker.lockID = migrationOracleLockID(scope)
	seconds := durationCeilingSeconds(timeout)
	if time.Duration(seconds)*time.Second > maximumOracleLockTimeout {
		seconds = int64(maximumOracleLockTimeout / time.Second)
	}
	// Oracle 23c 之前 SQL 层不能绑定 PL/SQL BOOLEAN；省略第四参数以使用 FALSE 默认值。
	rows, err = locker.session.QueryContext(ctx, "SELECT DBMS_LOCK.REQUEST(?, ?, ?) AS lock_result FROM dual", locker.lockID, oracleExclusiveLockMode, seconds)
	if err != nil {
		return fmt.Errorf("%w: Oracle DBMS_LOCK.REQUEST 执行失败，需要授予 EXECUTE DBMS_LOCK: %w", ErrMigrationLockUnavailable, err)
	}
	result, parseErr := migrationLockInt(rows, "lock_result")
	if parseErr != nil || result != 0 {
		return fmt.Errorf("%w: Oracle DBMS_LOCK.REQUEST 返回 %d", ErrMigrationLockUnavailable, result)
	}
	return nil
}

func (locker *oracleMigrationLocker) Release(ctx context.Context, _ bool) error {
	rows, err := locker.session.QueryContext(ctx, "SELECT DBMS_LOCK.RELEASE(?) AS lock_result FROM dual", locker.lockID)
	if err != nil {
		return fmt.Errorf("%w: Oracle DBMS_LOCK.RELEASE 执行失败: %w", ErrMigrationLockLost, err)
	}
	result, parseErr := migrationLockInt(rows, "lock_result")
	if parseErr != nil || result != 0 {
		return fmt.Errorf("%w: Oracle DBMS_LOCK.RELEASE 返回 %d", ErrMigrationLockLost, result)
	}
	return nil
}

type sqliteMigrationLocker struct {
	session             db.PinnedSQLConnection
	previousBusyTimeout int64
	busyTimeoutChanged  bool
}

func (locker *sqliteMigrationLocker) Acquire(ctx context.Context, _ time.Duration) error {
	rows, err := locker.session.QueryContext(ctx, "PRAGMA busy_timeout")
	if err != nil {
		return fmt.Errorf("%w: 读取 SQLite busy_timeout 失败: %w", ErrMigrationLockUnavailable, err)
	}
	locker.previousBusyTimeout, err = migrationOnlyInt(rows)
	if err != nil || locker.previousBusyTimeout < 0 {
		return fmt.Errorf("%w: SQLite busy_timeout 返回非法值", ErrMigrationLockUnavailable)
	}
	if _, err = locker.session.ExecuteContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		return fmt.Errorf("%w: 关闭 SQLite busy handler 失败: %w", ErrMigrationLockUnavailable, err)
	}
	locker.busyTimeoutChanged = true
	if err = locker.executeWithRetry(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("%w: SQLite BEGIN IMMEDIATE 失败: %w", ErrMigrationLockUnavailable, err)
	}
	return nil
}

func (locker *sqliteMigrationLocker) Release(ctx context.Context, commit bool) error {
	statement := "ROLLBACK"
	if commit {
		statement = "COMMIT"
	}
	var releaseErr error
	if commit {
		releaseErr = locker.executeWithRetry(ctx, statement)
	} else {
		_, releaseErr = locker.session.ExecuteContext(ctx, statement)
	}
	var restoreErr error
	if locker.busyTimeoutChanged {
		_, restoreErr = locker.session.ExecuteContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", locker.previousBusyTimeout))
		locker.busyTimeoutChanged = false
	}
	if releaseErr != nil || restoreErr != nil {
		return fmt.Errorf("%w: SQLite %s 或 busy_timeout 恢复失败: %w", ErrMigrationLockLost, statement, errors.Join(releaseErr, restoreErr))
	}
	return nil
}

func (locker *sqliteMigrationLocker) executeWithRetry(ctx context.Context, statement string) error {
	delay := sqliteLockRetryInitial
	for {
		_, err := locker.session.ExecuteContext(ctx, statement)
		if err == nil {
			return nil
		}
		if !isSQLiteBusyOrLocked(err) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
		if delay < sqliteLockRetryMaximum {
			delay *= 2
			if delay > sqliteLockRetryMaximum {
				delay = sqliteLockRetryMaximum
			}
		}
	}
}

func migrationLockKeyPair(value string) (int32, int32) {
	digest := sha256.Sum256([]byte(value))
	return signedMigrationLockKeyPart(binary.BigEndian.Uint32(digest[0:4])), signedMigrationLockKeyPart(binary.BigEndian.Uint32(digest[4:8]))
}

// signedMigrationLockKeyPart 将摘要的 32 位比特模式映射为 PostgreSQL
// advisory lock 使用的有符号参数，并避免超出 int32 上界的直接转换。
func signedMigrationLockKeyPart(value uint32) int32 {
	if value <= uint32(math.MaxInt32) {
		return int32(value)
	}
	return -int32(^value&uint32(math.MaxInt32)) - 1
}

func migrationMySQLLockName(scope string) string {
	digest := sha256.Sum256([]byte(migrationLockNamespace + ":" + scope))
	return "thinkgo:migration:" + hex.EncodeToString(digest[:20])
}

func migrationOracleLockID(scope string) int64 {
	digest := sha256.Sum256([]byte(migrationLockNamespace + ":" + scope))
	value := int64(binary.BigEndian.Uint32(digest[0:4]) & 0x3fffffff)
	if value == 0 {
		return 1
	}
	return value
}

func durationCeilingSeconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 1
	}
	seconds := int64((duration + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func durationCeilingMilliseconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 1
	}
	milliseconds := int64((duration + time.Millisecond - 1) / time.Millisecond)
	if milliseconds > maximumSQLServerLockMillis {
		return maximumSQLServerLockMillis
	}
	return milliseconds
}

func migrationLockValue(rows []map[string]interface{}, column string) (interface{}, error) {
	if len(rows) != 1 {
		return nil, ErrMigrationLockUnavailable
	}
	for key, value := range rows[0] {
		if strings.EqualFold(strings.TrimSpace(key), column) {
			return value, nil
		}
	}
	return nil, ErrMigrationLockUnavailable
}

func migrationLockInt(rows []map[string]interface{}, column string) (int64, error) {
	value, err := migrationLockValue(rows, column)
	if err != nil {
		return 0, err
	}
	return migrationInt64(value)
}

func migrationOnlyInt(rows []map[string]interface{}) (int64, error) {
	if len(rows) != 1 || len(rows[0]) != 1 {
		return 0, ErrMigrationLockUnavailable
	}
	for _, value := range rows[0] {
		return migrationInt64(value)
	}
	return 0, ErrMigrationLockUnavailable
}

func isSQLiteBusyOrLocked(err error) bool {
	for current := err; current != nil; current = errors.Unwrap(current) {
		value := reflect.ValueOf(current)
		if value.Kind() == reflect.Pointer && !value.IsNil() {
			value = value.Elem()
		}
		if value.IsValid() && value.Kind() == reflect.Struct {
			code := value.FieldByName("Code")
			if code.IsValid() && code.CanInt() {
				numeric := code.Int()
				if numeric == sqliteBusyResultCode || numeric == sqliteLockedResultCode {
					return true
				}
			}
		}
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked")
}

func migrationLockBool(rows []map[string]interface{}, column string) (bool, error) {
	value, err := migrationLockValue(rows, column)
	if err != nil {
		return false, err
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") || strings.TrimSpace(typed) == "1", nil
	case []byte:
		text := strings.TrimSpace(string(typed))
		return strings.EqualFold(text, "true") || text == "1", nil
	default:
		integer, integerErr := migrationInt64(value)
		if integerErr != nil {
			return false, integerErr
		}
		return integer == 1, nil
	}
}

func migrationLockText(rows []map[string]interface{}, column string) (string, error) {
	value, err := migrationLockValue(rows, column)
	if err != nil {
		return "", err
	}
	text, err := migrationText(value)
	if err != nil || strings.TrimSpace(text) == "" {
		return "", errors.Join(err, ErrMigrationLockUnavailable)
	}
	return strings.TrimSpace(text), nil
}
