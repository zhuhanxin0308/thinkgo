//go:build cgo

package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSQLiteNestedTransactionRejectsBeforeAcquiringHandle 验证单连接池上的同库事务嵌套立即失败。
func TestSQLiteNestedTransactionRejectsBeforeAcquiringHandle(t *testing.T) {
	database, _ := newSQLiteQuoteDatabase(t)
	outer, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Rollback()
	ctx, cancel := context.WithTimeout(outer.Context(), time.Second)
	defer cancel()
	err = database.TransactionContext(ctx, func(*Tx) error {
		t.Error("不应调用同库嵌套事务的业务回调")
		return nil
	})
	if !errors.Is(err, ErrNestedTransaction) || ctx.Err() != nil {
		t.Fatalf("嵌套事务应在等待连接前返回明确错误: %v", err)
	}
}

// TestSQLiteTransactionScopeDetectsSharedHandlesAndAncestors 验证分层传递、句柄别名和事务结束后的行为。
func TestSQLiteTransactionScopeDetectsSharedHandlesAndAncestors(t *testing.T) {
	first, connection := newSQLiteQuoteDatabase(t)
	second, _ := newSQLiteQuoteDatabase(t)
	if _, err := first.Execute("CREATE TABLE tx_scope (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	outer, err := first.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Rollback()
	ctx, cancel := context.WithTimeout(outer.Context(), time.Second)
	defer cancel()
	alias := NewDB(connection)
	if nested, err := alias.BeginTx(ctx, nil); !errors.Is(err, ErrNestedTransaction) || nested != nil {
		t.Fatalf("共享物理连接的不同 DB 外观也应拒绝嵌套: %v", err)
	}
	other, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("不同数据库可创建独立事务: %v", err)
	}
	defer other.Rollback()
	if nested, err := first.BeginTx(other.Context(), nil); !errors.Is(err, ErrNestedTransaction) || nested != nil {
		t.Fatalf("跨库调用后仍应识别祖先事务: %v", err)
	}
	if _, err := outer.ExecuteContext(ctx, "INSERT INTO tx_scope (id) VALUES (?)", 1); err != nil {
		t.Fatalf("拒绝嵌套后外层事务仍可使用: %v", err)
	}
	if err := outer.Rollback(); err != nil {
		t.Fatal(err)
	}
	if fresh, err := first.BeginTx(outer.Context(), nil); err != nil {
		t.Fatalf("已结束事务的上下文应允许新事务: %v", err)
	} else if err := fresh.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rows, err := first.Query("SELECT id FROM tx_scope"); err != nil || len(rows) != 0 {
		t.Fatalf("外层回滚应撤销写入: %v %v", rows, err)
	}
}
