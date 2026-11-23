//go:build cgo

package db

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

func TestSQLiteBatchTransactionOnAbnormalExit(t *testing.T) {
	for _, exit := range []string{"panic", "legacy_nil_panic", "goexit", "explicit_panic"} {
		t.Run(exit, func(t *testing.T) {
			database, connection := newSQLiteQuoteDatabase(t)
			if _, err := database.Execute("CREATE TABLE records (id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			// 外部截止时间保持有效；异常退出必须自行归还事务，不能等调用者取消。
			ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			query := database.Table("records").WithContext(ctx)
			var explicit *Tx
			if exit == "explicit_panic" {
				var err error
				explicit, err = database.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer explicit.Rollback()
				query = explicit.Table("records")
			}
			var panicValue interface{} = errors.New("方言构造第二批时退出")
			if exit == "legacy_nil_panic" {
				t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
				panicValue = nil
			}
			dialect := &batchTransactionBuilder{onRebind: func(batch int) {
				if batch == 2 {
					if exit == "goexit" {
						runtime.Goexit()
					}
					panic(panicValue)
				}
			}}
			connection.Builder = dialect
			rows := []map[string]interface{}{{"id": 1}, {"id": 2}}
			done := make(chan struct{})
			var returned, resumed bool
			var recovered interface{}
			go func() {
				defer close(done)
				func() {
					defer func() { recovered = recover() }()
					_, _ = query.InsertAll(rows)
					returned = true
				}()
				resumed = true
			}()
			<-done
			if returned || resumed != (exit != "goexit") || ctx.Err() != nil || dialect.batches != 2 || (exit == "goexit" && recovered != nil) || (exit != "goexit" && recovered != panicValue) {
				t.Fatalf("未保留第二批的退出方式：returned=%t resumed=%t panic=%v batches=%d ctx=%v", returned, resumed, recovered, dialect.batches, ctx.Err())
			}
			if explicit != nil {
				values, err := explicit.QueryContext(ctx, "SELECT id FROM records")
				if err != nil || len(values) != 1 || values[0]["id"] != int64(1) {
					t.Fatalf("显式事务的第一批应保留给调用者决定：%v %v", values, err)
				}
				if err := explicit.Rollback(); err != nil {
					t.Fatal(err)
				}
			}
			if inUse := connection.DB.Stats().InUse; inUse != 0 {
				t.Fatalf("调用者上下文仍有效时事务未归还连接：inUse=%d", inUse)
			}
			if values, err := database.QueryContext(ctx, "SELECT id FROM records"); err != nil || len(values) != 0 {
				t.Fatalf("已回滚的第一批仍然可见：%v %v", values, err)
			}
			if affected, err := database.Table("records").WithContext(ctx).InsertAll(rows); err != nil || affected != int64(len(rows)) {
				t.Fatalf("后续事务无法写入完整批次：%d %v", affected, err)
			}
			closeCtx, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			if err := database.CloseContext(closeCtx); err != nil {
				t.Fatalf("事务退出后关闭数据库仍被阻塞：%v", err)
			}
		})
	}
}

func TestSQLiteImplicitBatchReportsFinalizationErrors(t *testing.T) {
	database, connection := newSQLiteQuoteDatabase(t)
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
		"CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) DEFERRABLE INITIALLY DEFERRED)",
	} {
		if _, err := connection.DB.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	connection.Builder = &batchTransactionBuilder{}
	rows := []map[string]interface{}{{"parent_id": 1}, {"parent_id": 2}}
	logger := &captureTxErrorLogger{}
	database.SetLogger(logger)

	outer, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Rollback()
	if affected, err := database.Table("children").WithContext(outer.Context()).InsertAll(rows); affected != 0 || !errors.Is(err, ErrNestedTransaction) {
		t.Fatalf("隐式事务开启失败时未返回原错误与零行数：%d %v", affected, err)
	}
	if err := outer.Rollback(); err != nil {
		t.Fatal(err)
	}
	// 两批写入均执行成功，SQLite 在提交时才检查延迟外键约束。
	var constraint sqlite3.Error
	if affected, err := database.Table("children").InsertAll(rows); affected != 0 || !errors.As(err, &constraint) || constraint.ExtendedCode != sqlite3.ErrConstraintForeignKey {
		t.Fatalf("提交失败时未返回驱动错误与零行数：%d %v", affected, err)
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.operations) != 2 || logger.operations[0] != "insert_all" || logger.operations[1] != "insert_all" {
		t.Errorf("开启和提交错误应各记录一次：%v", logger.operations)
	}
}
