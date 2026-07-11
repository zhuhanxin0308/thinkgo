package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Tx 表示一个数据库事务上下文。
type Tx struct {
	db  *DB
	tx  *sql.Tx
	ctx context.Context // 事务上下文，自动传播给经 Tx.Table/Name 创建的查询
}

// Begin 开启事务（使用 context.Background()）。
// 对应 ThinkPHP 的 Db::startTrans()
func (db *DB) Begin() (*Tx, error) {
	return db.BeginTx(context.Background(), nil)
}

// BeginTx 以指定上下文与隔离级别开启事务。
// ctx 可用于在请求取消/超时时回滚事务；opts 可指定隔离级别与只读属性。
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sqlConn, ok := db.connection.(*SQLConnection)
	if !ok {
		return nil, fmt.Errorf("当前数据库连接不支持事务操作")
	}
	tx, err := sqlConn.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{db: db, tx: tx, ctx: ctx}, nil
}

// Transaction 执行一个闭包事务，自动处理 Commit 和 Rollback。
// 对应 ThinkPHP 的 Db::transaction(function() { ... })
func (db *DB) Transaction(fn func(tx *Tx) error) error {
	return db.TransactionContext(context.Background(), fn)
}

// TransactionContext 在指定上下文下执行闭包事务，自动 Commit/Rollback，
// 且把 ctx 传播给事务内经 Tx.Table/Name 创建的查询。
func (db *DB) TransactionContext(ctx context.Context, fn func(tx *Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// Commit 提交事务。提交后置空底层句柄，避免重复提交触发 sql.ErrTxDone。
func (t *Tx) Commit() error {
	if t.tx == nil {
		return fmt.Errorf("事务未初始化或已结束")
	}
	err := t.tx.Commit()
	t.tx = nil
	return err
}

// Rollback 回滚事务。回滚后置空底层句柄，避免重复回滚。
func (t *Tx) Rollback() error {
	if t.tx == nil {
		return fmt.Errorf("事务未初始化或已结束")
	}
	err := t.tx.Rollback()
	t.tx = nil
	return err
}

// bindQuery 将查询绑定到当前事务；事务结束后创建的查询必须失败，
// 不能因为 tx 为空而退回普通连接执行，避免突破事务边界。
func (t *Tx) bindQuery(q *Query) *Query {
	if t.tx == nil {
		return q.setError(fmt.Errorf("事务未初始化或已结束"))
	}
	q.txExecutor = t.tx
	q.ctx = t.ctx
	return q
}

// Table 使用完整表名创建绑定事务的查询构建器（不自动拼接前缀）。
// 对应 ThinkPHP 的 Tx.Table()
func (t *Tx) Table(name string) *Query {
	q := newQuery(t.db, name, true)
	return t.bindQuery(q)
}

// Name 使用短表名创建绑定事务的查询构建器（自动拼接配置的表前缀）。
// 对应 ThinkPHP 的 Tx.Name()
func (t *Tx) Name(name string) *Query {
	q := newQuery(t.db, name)
	return t.bindQuery(q)
}
