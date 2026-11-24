package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cleanup"
)

type transactionState uint8

const (
	transactionActive transactionState = iota
	transactionCompleting
	transactionCommitted
	transactionRolledBack
	transactionContextRolledBack
	transactionFinalizationFailed
)

type transactionAction uint8

const (
	actionCommit transactionAction = iota
	actionRollback
	actionContextRollback
)

type activeTransactionContextKey struct{}

// Tx 表示持有数据库生命周期租约的 SQL 事务。
type Tx struct {
	db                     *DB
	tx                     *sql.Tx
	cancelSQLTx            context.CancelFunc
	connection             *SQLConnection
	ctx                    context.Context
	outer                  *Tx
	mu                     sync.Mutex
	state                  transactionState
	managedByClosure       bool
	contextRollbackStarted bool
	contextRollbackDone    chan struct{}
	contextRollbackErr     error
	done                   chan struct{}
	release                func()
	releaseOnce            sync.Once
}

func (db *DB) Begin() (*Tx, error) {
	return db.BeginTx(context.Background(), nil)
}

// BeginTx 开启事务；事务结束前 DB.Close 会等待其释放连接租约。
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	return db.beginTx(ctx, opts, false)
}

func (db *DB) beginTx(ctx context.Context, opts *sql.TxOptions, managedByClosure bool) (*Tx, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: 事务上下文不能为空", ErrInvalidTransaction)
	}
	connection, release, err := db.acquireConnection()
	if err != nil {
		return nil, err
	}
	sqlConnection, ok := connection.(*SQLConnection)
	if !ok || sqlConnection == nil {
		release()
		return nil, fmt.Errorf("%w: 当前连接不支持 SQL 事务", ErrInvalidTransaction)
	}
	if err := sqlConnection.validateContext(ctx); err != nil {
		release()
		return nil, err
	}
	outer, _ := ctx.Value(activeTransactionContextKey{}).(*Tx)
	for active := outer; active != nil; active = active.outer {
		if active.db != db && active.connection.DB != sqlConnection.DB {
			continue
		}
		select {
		case <-active.done:
		default:
			release()
			return nil, ErrNestedTransaction
		}
	}
	// 仅在建连与开启事务阶段联动外部取消；事务建立后由 Tx.watchContext 独占驱动回滚，
	// 避免标准库 sql.Tx.awaitDone 抢占状态并静默丢弃底层驱动 Rollback 错误。
	sqlTxCtx, cancelSQLTx := context.WithCancel(context.WithoutCancel(ctx))
	stopBeginCancel := context.AfterFunc(ctx, cancelSQLTx)
	handle, err := sqlConnection.DB.BeginTx(sqlTxCtx, opts)
	if !stopBeginCancel() {
		cancelSQLTx()
		if err == nil {
			rollbackErr := handle.Rollback()
			if errors.Is(rollbackErr, sql.ErrTxDone) {
				rollbackErr = nil
			}
			release()
			return nil, errors.Join(ctx.Err(), rollbackErr)
		}
		release()
		return nil, err
	}
	if err != nil {
		cancelSQLTx()
		release()
		return nil, err
	}
	transaction := &Tx{
		db:                  db,
		tx:                  handle,
		cancelSQLTx:         cancelSQLTx,
		connection:          sqlConnection,
		outer:               outer,
		state:               transactionActive,
		managedByClosure:    managedByClosure,
		contextRollbackDone: make(chan struct{}),
		done:                make(chan struct{}),
		release:             release,
	}
	transaction.ctx = context.WithValue(ctx, activeTransactionContextKey{}, transaction)
	go transaction.watchContext()
	return transaction, nil
}

// Context 返回携带当前事务作用域的上下文，供下游调用识别同库事务。
func (t *Tx) Context() context.Context {
	return t.ctx
}

func (db *DB) Transaction(fn func(tx *Tx) error) error {
	return db.TransactionContext(context.Background(), fn)
}

// TransactionContext 执行闭包事务，并同时保留业务错误与回滚错误。
func (db *DB) TransactionContext(ctx context.Context, fn func(tx *Tx) error) error {
	if ctx == nil || fn == nil {
		return fmt.Errorf("%w: 事务上下文和回调不能为空", ErrInvalidTransaction)
	}
	transaction, err := db.beginTx(ctx, nil, true)
	if err != nil {
		return err
	}

	return cleanup.Call(func() error {
		if callbackErr := fn(transaction); callbackErr != nil {
			return errors.Join(callbackErr, transaction.rollbackAfterCallback())
		}
		return transaction.commitAfterCallback()
	}, func(panicked bool) {
		if rollbackErr := transaction.rollbackAfterCallback(); rollbackErr != nil && !errors.Is(rollbackErr, ErrTransactionDone) {
			operation := "rollback_after_callback_exit"
			if panicked {
				operation = "rollback_after_panic"
			}
			_ = db.reportError(operation, rollbackErr, nil)
		}
	})
}

func (t *Tx) Commit() error {
	return t.finalize(actionCommit)
}

func (t *Tx) Rollback() error {
	return t.finalize(actionRollback)
}

func (t *Tx) waitContextRollback() (bool, error) {
	t.mu.Lock()
	started := t.contextRollbackStarted || t.state == transactionContextRolledBack
	done := t.contextRollbackDone
	t.mu.Unlock()
	if !started {
		return false, nil
	}
	if done != nil {
		<-done
	}
	t.mu.Lock()
	rollbackErr := t.contextRollbackErr
	t.mu.Unlock()
	return true, rollbackErr
}

func (t *Tx) rollbackAfterCallback() error {
	if t == nil {
		return fmt.Errorf("%w: 事务不能为空", ErrInvalidTransaction)
	}
	if started, rollbackErr := t.waitContextRollback(); started {
		return rollbackErr
	}
	err := t.finalize(actionRollback)
	if errors.Is(err, ErrTransactionDone) {
		if started, rollbackErr := t.waitContextRollback(); started {
			return rollbackErr
		}
	}
	if t.ctx != nil && t.ctx.Err() != nil && errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

func (t *Tx) commitAfterCallback() error {
	err := t.finalize(actionCommit)
	if errors.Is(err, ErrTransactionDone) {
		if started, rollbackErr := t.waitContextRollback(); started {
			return errors.Join(t.ctx.Err(), rollbackErr)
		}
	}
	return err
}

// Done closes exactly once when the transaction wrapper reaches a terminal state.
func (t *Tx) Done() <-chan struct{} {
	if t == nil {
		return nil
	}
	return t.done
}

func (t *Tx) watchContext() {
	select {
	case <-t.ctx.Done():
		err := t.finalize(actionContextRollback)
		if err != nil && !errors.Is(err, ErrTransactionDone) && !t.managedByClosure && t.db != nil {
			_ = t.db.reportError("context_rollback", err, nil)
		}
	case <-t.done:
	}
}

func (t *Tx) finalize(action transactionAction) error {
	if t == nil {
		return fmt.Errorf("%w: 事务不能为空", ErrInvalidTransaction)
	}
	t.mu.Lock()
	if t.state != transactionActive || t.tx == nil {
		t.mu.Unlock()
		return ErrTransactionDone
	}
	canceledCommit := false
	if action == actionCommit && t.ctx != nil && t.ctx.Err() != nil {
		action = actionContextRollback
		canceledCommit = true
	}
	t.state = transactionCompleting
	if action == actionContextRollback {
		t.contextRollbackStarted = true
	}
	handle := t.tx
	cancelSQLTx := t.cancelSQLTx
	t.mu.Unlock()

	var err error
	if action == actionCommit {
		err = handle.Commit()
	} else {
		err = handle.Rollback()
	}
	if cancelSQLTx != nil {
		cancelSQLTx()
	}

	var contextBackendErr error
	if action == actionContextRollback && err != nil && !errors.Is(err, sql.ErrTxDone) {
		contextBackendErr = err
	}

	t.mu.Lock()
	switch {
	case action == actionContextRollback:
		t.contextRollbackErr = contextBackendErr
		if contextBackendErr == nil {
			t.state = transactionContextRolledBack
		} else {
			t.state = transactionFinalizationFailed
		}
	case action == actionCommit && err == nil:
		t.state = transactionCommitted
	case action == actionRollback && err == nil:
		t.state = transactionRolledBack
	default:
		t.state = transactionFinalizationFailed
	}
	if action == actionContextRollback && t.contextRollbackDone != nil {
		close(t.contextRollbackDone)
	}
	if t.done != nil {
		close(t.done)
	}
	t.mu.Unlock()
	t.releaseOnce.Do(func() {
		if t.release != nil {
			t.release()
		}
	})
	if canceledCommit {
		return errors.Join(t.ctx.Err(), contextBackendErr)
	}
	if action == actionContextRollback {
		return contextBackendErr
	}
	return err
}

func (t *Tx) active() error {
	if t == nil {
		return fmt.Errorf("%w: 事务不能为空", ErrInvalidTransaction)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != transactionActive || t.tx == nil || t.connection == nil {
		return ErrTransactionDone
	}
	return nil
}

// QueryContext 在当前事务内执行经过校验和方言重绑定的原生查询。
func (t *Tx) QueryContext(ctx context.Context, rawSQL string, args ...interface{}) ([]map[string]interface{}, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: 查询上下文不能为空", ErrInvalidTransaction)
	}
	if err := t.active(); err != nil {
		return nil, err
	}
	if err := validateRawStatement(rawSQL, len(args), sqlBuilderDialect(t.connection.Builder)); err != nil {
		return nil, err
	}
	if err := validateBindParameterBudget(t.connection.Builder, len(args)); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, t.connection.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return nil, err
	}
	return scanSQLRows(rows, t.connection.Builder, t.connection.Location())
}

// ExecuteContext 在当前事务内执行经过校验和方言重绑定的原生命令。
func (t *Tx) ExecuteContext(ctx context.Context, rawSQL string, args ...interface{}) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("%w: 执行上下文不能为空", ErrInvalidTransaction)
	}
	if err := t.active(); err != nil {
		return 0, err
	}
	if err := validateRawStatement(rawSQL, len(args), sqlBuilderDialect(t.connection.Builder)); err != nil {
		return 0, err
	}
	if err := validateBindParameterBudget(t.connection.Builder, len(args)); err != nil {
		return 0, err
	}
	result, err := t.tx.ExecContext(ctx, t.connection.Builder.Rebind(rawSQL), args...)
	if err != nil {
		return 0, err
	}
	return sqlRowsAffected(result)
}

// bindQuery 绑定事务所有者，使结束后的查询不会退回普通连接执行。
func (t *Tx) bindQuery(query *Query) *Query {
	if query == nil {
		return query
	}
	// 即使事务已经结束，也保留来源，派生关联不能误认成普通查询。
	if t != nil {
		query.txOwner = t
		query.ctx = t.ctx
	}
	if err := t.active(); err != nil {
		return query.setError(err)
	}
	query.txExecutor = t.tx
	return query
}

func (t *Tx) Table(name string) *Query {
	if t == nil {
		return newQuery(nil, name, true).setError(ErrInvalidTransaction)
	}
	return newQueryWithOwner(t.db, name, true, t)
}

func (t *Tx) Name(name string) *Query {
	if t == nil {
		return newQuery(nil, name).setError(ErrInvalidTransaction)
	}
	return newQueryWithOwner(t.db, name, false, t)
}
