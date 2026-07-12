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
	transactionDone
)

// Tx 表示持有数据库生命周期租约的 SQL 事务。
type Tx struct {
	db          *DB
	tx          *sql.Tx
	connection  *SQLConnection
	ctx         context.Context
	mu          sync.Mutex
	state       transactionState
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
	return &Tx{
		db:         db,
		tx:         handle,
		connection: sqlConnection,
		ctx:        ctx,
		state:      transactionActive,
		release:    release,
	}, nil
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
	return t.complete(true)
}

func (t *Tx) Rollback() error {
	return t.complete(false)
}

func (t *Tx) complete(commit bool) error {
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
	if commit {
		err = handle.Commit()
	} else {
		err = handle.Rollback()
	}

	t.mu.Lock()
	t.state = transactionDone
	t.mu.Unlock()
	t.releaseOnce.Do(func() {
		if t.release != nil {
			t.release()
		}
	})
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
