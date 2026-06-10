package db

import (
	"database/sql"
	"fmt"
)

// Tx 表示一个数据库事务上下文。
type Tx struct {
	db *DB
	tx *sql.Tx
}

// Begin 开启事务。
// 对应 ThinkPHP 的 Db::startTrans()
func (db *DB) Begin() (*Tx, error) {
	sqlConn, ok := db.connection.(*SQLConnection)
	if !ok {
		return nil, fmt.Errorf("当前数据库连接不支持事务操作")
	}
	tx, err := sqlConn.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{
		db: db,
		tx: tx,
	}, nil
}

// Transaction 执行一个闭包事务，自动处理 Commit 和 Rollback。
// 对应 ThinkPHP 的 Db::transaction(function() { ... })
func (db *DB) Transaction(fn func(tx *Tx) error) error {
	tx, err := db.Begin()
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

// Commit 提交事务。
func (t *Tx) Commit() error {
	if t.tx == nil {
		return fmt.Errorf("事务未初始化或已结束")
	}
	return t.tx.Commit()
}

// Rollback 回滚事务。
func (t *Tx) Rollback() error {
	if t.tx == nil {
		return fmt.Errorf("事务未初始化或已结束")
	}
	return t.tx.Rollback()
}

// Table 使用完整表名创建绑定事务的查询构建器（不自动拼接前缀）。
// 对应 ThinkPHP 的 Tx.Table()
func (t *Tx) Table(name string) *Query {
	q := newQuery(t.db, name, true)
	q.txExecutor = t.tx
	return q
}

// Name 使用短表名创建绑定事务的查询构建器（自动拼接配置的表前缀）。
// 对应 ThinkPHP 的 Tx.Name()
func (t *Tx) Name(name string) *Query {
	q := newQuery(t.db, name)
	q.txExecutor = t.tx
	return q
}

