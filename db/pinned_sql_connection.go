package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// PinnedSQLConnection 表示在 callback 生命周期内固定到同一物理 SQL 会话的原生执行器。
// Invalidate 会幂等地禁止后续操作，并要求 WithPinnedSQLConnection 丢弃底层物理连接。
type PinnedSQLConnection interface {
	ContextualRawQueryable
	DialectName() string
	TransactionContext(context.Context, func(ContextualRawQueryable) error) error
	Invalidate()
}

type pinnedSQLConnection struct {
	database   *DB
	connection *SQLConnection
	handle     *sql.Conn
	dialect    string

	stateMu     sync.Mutex
	active      sync.WaitGroup
	closing     bool
	invalidated bool
}

// WithPinnedSQLConnection 在一个受 DB 生命周期租约保护的物理 SQL 连接上执行 callback。
// callback 返回、panic 或上下文取消后都会先停止新操作，再安全归还或丢弃物理连接。
func (database *DB) WithPinnedSQLConnection(ctx context.Context, callback func(PinnedSQLConnection) error) (resultErr error) {
	if database == nil {
		return ErrDatabaseUnavailable
	}
	if ctx == nil || callback == nil {
		return ErrInvalidDatabaseContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	connection, release, err := database.acquireConnection()
	if err != nil {
		return err
	}
	sqlConnection, ok := connection.(*SQLConnection)
	if !ok || sqlConnection == nil || sqlConnection.DB == nil || isNilDatabaseDependency(sqlConnection.Builder) {
		release()
		return fmt.Errorf("%w: 当前连接不是 SQL 连接", ErrDatabaseUnavailable)
	}
	handle, err := sqlConnection.DB.Conn(ctx)
	if err != nil {
		release()
		return err
	}
	pinned := &pinnedSQLConnection{
		database:   database,
		connection: sqlConnection,
		handle:     handle,
		dialect:    normalizePinnedDialect(sqlConnection.Builder.DialectName()),
	}
	defer release()

	var panicValue interface{}
	func() {
		defer func() {
			panicValue = recover()
		}()
		resultErr = callback(pinned)
	}()
	if panicValue != nil {
		pinned.Invalidate()
		if cleanupErr := pinned.finish(); cleanupErr != nil {
			_ = database.reportError("pinned_cleanup_after_panic", cleanupErr, nil)
		}
		panic(panicValue)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		resultErr = errors.Join(resultErr, ctxErr)
	}
	return errors.Join(resultErr, pinned.finish())
}

func normalizePinnedDialect(dialect string) string {
	return strings.ToLower(strings.TrimSpace(dialect))
}

func (connection *pinnedSQLConnection) DialectName() string {
	if connection == nil {
		return ""
	}
	return connection.dialect
}

func (connection *pinnedSQLConnection) Invalidate() {
	if connection == nil {
		return
	}
	connection.stateMu.Lock()
	connection.invalidated = true
	connection.stateMu.Unlock()
}

func (connection *pinnedSQLConnection) beginOperation() error {
	if connection == nil || connection.handle == nil || connection.connection == nil {
		return ErrDatabaseUnavailable
	}
	connection.stateMu.Lock()
	defer connection.stateMu.Unlock()
	if connection.closing || connection.invalidated {
		return ErrPinnedConnectionInvalidated
	}
	connection.active.Add(1)
	return nil
}

func (connection *pinnedSQLConnection) endOperation() {
	connection.active.Done()
}

func (connection *pinnedSQLConnection) usable() error {
	if connection == nil || connection.handle == nil {
		return ErrDatabaseUnavailable
	}
	connection.stateMu.Lock()
	defer connection.stateMu.Unlock()
	if connection.closing || connection.invalidated {
		return ErrPinnedConnectionInvalidated
	}
	return nil
}

func (connection *pinnedSQLConnection) QueryContext(ctx context.Context, rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	if err := connection.beginOperation(); err != nil {
		return nil, err
	}
	defer connection.endOperation()
	if ctx == nil {
		return nil, fmt.Errorf("%w: pinned 查询上下文不能为空", ErrInvalidQuery)
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return nil, err
	}
	if err := validateBindParameterBudget(connection.connection.Builder, len(args)); err != nil {
		return nil, err
	}
	rows, err := connection.handle.QueryContext(ctx, connection.connection.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return nil, err
	}
	return scanSQLRows(rows, connection.connection.Builder, connection.connection.Location())
}

func (connection *pinnedSQLConnection) ExecuteContext(ctx context.Context, rawSQL string, args ...interface{}) (int64, error) {
	if err := connection.beginOperation(); err != nil {
		return 0, err
	}
	defer connection.endOperation()
	if ctx == nil {
		return 0, fmt.Errorf("%w: pinned 执行上下文不能为空", ErrInvalidQuery)
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return 0, err
	}
	if err := validateBindParameterBudget(connection.connection.Builder, len(args)); err != nil {
		return 0, err
	}
	result, err := connection.handle.ExecContext(ctx, connection.connection.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (connection *pinnedSQLConnection) TransactionContext(ctx context.Context, callback func(ContextualRawQueryable) error) (resultErr error) {
	if ctx == nil || callback == nil {
		return fmt.Errorf("%w: pinned 事务上下文和回调不能为空", ErrInvalidTransaction)
	}
	if err := connection.beginOperation(); err != nil {
		return err
	}
	defer connection.endOperation()
	handle, err := connection.handle.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	transaction := &pinnedSQLTransaction{handle: handle, connection: connection.connection, owner: connection}
	defer func() {
		if recovered := recover(); recovered != nil {
			transaction.close()
			if rollbackErr := handle.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				_ = connection.database.reportError("pinned_rollback_after_panic", rollbackErr, nil)
			}
			panic(recovered)
		}
	}()
	if callbackErr := callback(transaction); callbackErr != nil {
		transaction.close()
		return errors.Join(callbackErr, handle.Rollback())
	}
	if ownerErr := connection.usable(); ownerErr != nil {
		transaction.close()
		return errors.Join(ownerErr, handle.Rollback())
	}
	transaction.close()
	return handle.Commit()
}

func (connection *pinnedSQLConnection) finish() error {
	if connection == nil || connection.handle == nil {
		return nil
	}
	connection.stateMu.Lock()
	connection.closing = true
	connection.stateMu.Unlock()
	connection.active.Wait()
	// 在途操作可能在 finish 等待期间发现锁丢失并调用 Invalidate，必须在等待后再取最终状态。
	connection.stateMu.Lock()
	discard := connection.invalidated
	connection.stateMu.Unlock()
	if !discard {
		return connection.handle.Close()
	}
	rawErr := connection.handle.Raw(func(interface{}) error {
		return driver.ErrBadConn
	})
	if errors.Is(rawErr, driver.ErrBadConn) {
		rawErr = nil
	}
	closeErr := connection.handle.Close()
	if errors.Is(closeErr, sql.ErrConnDone) {
		closeErr = nil
	}
	return errors.Join(rawErr, closeErr)
}

type pinnedSQLTransaction struct {
	handle     *sql.Tx
	connection *SQLConnection
	owner      *pinnedSQLConnection
	closed     sync.Once
	done       bool
	stateMu    sync.RWMutex
}

func (transaction *pinnedSQLTransaction) close() {
	if transaction == nil {
		return
	}
	transaction.closed.Do(func() {
		transaction.stateMu.Lock()
		transaction.done = true
		transaction.stateMu.Unlock()
	})
}

func (transaction *pinnedSQLTransaction) active() error {
	if transaction == nil || transaction.handle == nil || transaction.connection == nil {
		return ErrDatabaseUnavailable
	}
	transaction.stateMu.RLock()
	defer transaction.stateMu.RUnlock()
	if transaction.done {
		return ErrTransactionDone
	}
	if transaction.owner != nil {
		if err := transaction.owner.usable(); err != nil {
			return err
		}
	}
	return nil
}

func (transaction *pinnedSQLTransaction) QueryContext(ctx context.Context, rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: pinned 事务查询上下文不能为空", ErrInvalidTransaction)
	}
	if err := transaction.active(); err != nil {
		return nil, err
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return nil, err
	}
	if err := validateBindParameterBudget(transaction.connection.Builder, len(args)); err != nil {
		return nil, err
	}
	rows, err := transaction.handle.QueryContext(ctx, transaction.connection.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return nil, err
	}
	return scanSQLRows(rows, transaction.connection.Builder, transaction.connection.Location())
}

func (transaction *pinnedSQLTransaction) ExecuteContext(ctx context.Context, rawSQL string, args ...interface{}) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("%w: pinned 事务执行上下文不能为空", ErrInvalidTransaction)
	}
	if err := transaction.active(); err != nil {
		return 0, err
	}
	if err := validateRawStatement(rawSQL, len(args)); err != nil {
		return 0, err
	}
	if err := validateBindParameterBudget(transaction.connection.Builder, len(args)); err != nil {
		return 0, err
	}
	result, err := transaction.handle.ExecContext(ctx, transaction.connection.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
