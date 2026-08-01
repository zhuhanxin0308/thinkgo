package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
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

// Tx 表示持有数据库生命周期租约的 SQL 事务。
type Tx struct {
	db          *DB
	tx          *sql.Tx
	connection  *SQLConnection
	ctx         context.Context
	mu          sync.Mutex
	state       transactionState
	done        chan struct{}
	release     func()
	releaseOnce sync.Once
}

func (db *DB) Begin() (*Tx, error) {
	return db.BeginTx(context.Background(), nil)
}

// BeginTx 开启事务；事务结束前 DB.Close 会等待其释放连接租约。
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
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
	handle, err := sqlConnection.DB.BeginTx(ctx, opts)
	if err != nil {
		release()
		return nil, err
	}
	transaction := &Tx{
		db:         db,
		tx:         handle,
		connection: sqlConnection,
		ctx:        ctx,
		state:      transactionActive,
		done:       make(chan struct{}),
		release:    release,
	}
	go transaction.watchContext()
	return transaction, nil
}

func (db *DB) Transaction(fn func(tx *Tx) error) error {
	return db.TransactionContext(context.Background(), fn)
}

// TransactionContext 执行闭包事务，并同时保留业务错误与回滚错误。
func (db *DB) TransactionContext(ctx context.Context, fn func(tx *Tx) error) error {
	if ctx == nil || fn == nil {
		return fmt.Errorf("%w: 事务上下文和回调不能为空", ErrInvalidTransaction)
	}
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			if rollbackErr := transaction.Rollback(); rollbackErr != nil {
				_ = db.reportError("rollback_after_panic", rollbackErr, nil)
			}
			panic(recovered)
		}
	}()

	if callbackErr := fn(transaction); callbackErr != nil {
		return errors.Join(callbackErr, transaction.Rollback())
	}
	return transaction.Commit()
}

func (t *Tx) Commit() error {
	return t.finalize(actionCommit)
}

func (t *Tx) Rollback() error {
	return t.finalize(actionRollback)
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
		_ = t.finalize(actionContextRollback)
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
	t.state = transactionCompleting
	handle := t.tx
	t.mu.Unlock()

	var err error
	if action == actionCommit {
		err = handle.Commit()
	} else {
		err = handle.Rollback()
	}

	t.mu.Lock()
	switch {
	case action == actionContextRollback:
		t.state = transactionContextRolledBack
	case action == actionCommit && err == nil:
		t.state = transactionCommitted
	case action == actionCommit && t.ctx.Err() != nil:
		t.state = transactionContextRolledBack
	case action == actionRollback && err == nil:
		t.state = transactionRolledBack
	default:
		t.state = transactionFinalizationFailed
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
	if action == actionContextRollback && errors.Is(err, sql.ErrTxDone) {
		return t.ctx.Err()
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

// bindQuery 绑定事务所有者，使结束后的查询不会退回普通连接执行。
func (t *Tx) bindQuery(query *Query) *Query {
	if query == nil {
		return query
	}
	if err := t.active(); err != nil {
		return query.setError(err)
	}
	query.txExecutor = t.tx
	query.txOwner = t
	query.ctx = t.ctx
	return query
}

func (t *Tx) Table(name string) *Query {
	if t == nil {
		return newQuery(nil, name, true).setError(ErrInvalidTransaction)
	}
	return t.bindQuery(newQuery(t.db, name, true))
}

func (t *Tx) Name(name string) *Query {
	if t == nil {
		return newQuery(nil, name).setError(ErrInvalidTransaction)
	}
	return t.bindQuery(newQuery(t.db, name))
}
