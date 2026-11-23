package db

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"
)

// 旧模式的 nil panic 可恢复，Goexit 则结束协程；两者的回滚和连接丢弃都要先于调用方收尾。
func TestDatabaseCallbacksPreserveLegacyNilPanic(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	for _, test := range []struct {
		name    string
		pinned  bool
		tx      bool
		goexit  bool
		wantLog string
	}{
		{name: "transaction", tx: true, wantLog: "rollback_after_panic"},
		{name: "pinned_connection", pinned: true},
		{name: "pinned_transaction", pinned: true, tx: true, wantLog: "pinned_rollback_after_panic"},
		{name: "transaction_goexit", tx: true, goexit: true, wantLog: "rollback_after_callback_exit"},
		{name: "pinned_transaction_goexit", pinned: true, tx: true, goexit: true, wantLog: "pinned_rollback_after_callback_exit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, state := newPinnedSQLTestDatabase(t)
			state.rollbackErr = errors.New("驱动回滚失败")
			logger := &captureTxErrorLogger{}
			database.SetLogger(logger)
			exit := func() error {
				if test.goexit {
					runtime.Goexit()
				}
				panic(nil)
			}
			var recovered interface{}
			var returned, resumed bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				func() {
					defer func() { recovered = recover() }()
					if !test.pinned {
						_ = database.TransactionContext(t.Context(), func(*Tx) error { return exit() })
					} else {
						_ = database.WithPinnedSQLConnection(t.Context(), func(connection PinnedSQLConnection) error {
							if !test.tx {
								return exit()
							}
							return connection.TransactionContext(t.Context(), func(ContextualRawQueryable) error { return exit() })
						})
					}
					returned = true
				}()
				resumed = true
			}()
			<-done
			if returned || resumed == test.goexit || recovered != nil {
				t.Errorf("回调退出方式错误：returned=%t resumed=%t panic=%v", returned, resumed, recovered)
			}
			opened, closed, operations := state.snapshot()
			if len(opened) != 1 || (test.pinned && (len(closed) != 1 || closed[0] != opened[0])) {
				t.Errorf("异常退出后物理连接状态错误：opened=%v closed=%v", opened, closed)
			}
			if test.tx && (len(operations) != 2 || operations[0].query != "BEGIN" || operations[1].query != "ROLLBACK") {
				t.Errorf("异常事务未恰好回滚一次：%v", operations)
			}
			if test.tx && (len(logger.operations) != 1 || logger.operations[0] != test.wantLog) {
				t.Errorf("回滚失败的原因记录错误：%v", logger.operations)
			}
		})
	}
}

// TestTransactionGoexitReleasesLease covers goroutine termination, which is not a panic.
func TestTransactionGoexitReleasesLease(t *testing.T) {
	database, state := newFinalizationCountingDatabase(t)
	var transaction *Tx
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = database.Transaction(func(tx *Tx) error {
			transaction = tx
			runtime.Goexit()
			return nil
		})
	}()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("transaction goroutine did not exit")
	}
	// Also clean up the broken baseline without stranding its context watcher.
	t.Cleanup(func() {
		if transaction != nil {
			_ = transaction.Rollback()
		}
		_ = database.Close()
	})
	if transaction == nil {
		t.Fatal("callback was not entered")
	}
	select {
	case <-transaction.Done():
	default:
		t.Error("Goexit left the transaction and DB lifecycle lease active")
	}
	if got := state.rollbackCalls.Load(); got != 1 {
		t.Errorf("rollback calls = %d, want 1", got)
	}
	if got := state.commitCalls.Load(); got != 0 {
		t.Errorf("commit calls = %d, want 0", got)
	}
	if got := transaction.connection.DB.Stats().InUse; got != 0 {
		t.Errorf("connections still in use = %d", got)
	}
}

// TestPinnedConnectionGoexitReleasesHandle also verifies fail-closed reuse of the escaped wrapper.
func TestPinnedConnectionGoexitReleasesHandle(t *testing.T) {
	database, state := newPinnedSQLTestDatabase(t)
	var pinned *pinnedSQLConnection
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
			pinned = connection.(*pinnedSQLConnection)
			runtime.Goexit()
			return nil
		})
	}()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("pinned connection goroutine did not exit")
	}
	t.Cleanup(func() {
		if pinned != nil {
			_ = pinned.finish()
		}
	})
	if pinned == nil {
		t.Fatal("callback was not entered")
	}
	_, closed, _ := state.snapshot()
	if len(closed) != 1 {
		t.Errorf("abnormally abandoned physical connections closed = %d, want 1", len(closed))
	}
	if got := pinned.connection.DB.Stats().InUse; got != 0 {
		t.Errorf("physical connections still in use = %d", got)
	}
	if err := pinned.usable(); !errors.Is(err, ErrPinnedConnectionInvalidated) {
		t.Errorf("escaped connection remains usable: %v", err)
	}
}

// TestPinnedTransactionGoexitRollsBack checks cleanup of both nested ownership levels.
func TestPinnedTransactionGoexitRollsBack(t *testing.T) {
	database, state := newPinnedSQLTestDatabase(t)
	var pinned *pinnedSQLConnection
	var transaction *pinnedSQLTransaction
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
			pinned = connection.(*pinnedSQLConnection)
			return connection.TransactionContext(context.Background(), func(raw ContextualRawQueryable) error {
				transaction = raw.(*pinnedSQLTransaction)
				runtime.Goexit()
				return nil
			})
		})
	}()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("pinned transaction goroutine did not exit")
	}
	t.Cleanup(func() {
		if transaction != nil {
			_ = transaction.handle.Rollback()
		}
		if pinned != nil {
			_ = pinned.finish()
		}
	})
	if transaction == nil {
		t.Fatal("transaction callback was not entered")
	}
	_, closed, operations := state.snapshot()
	rollbacks, commits := 0, 0
	for _, operation := range operations {
		if operation.query == "ROLLBACK" {
			rollbacks++
		}
		if operation.query == "COMMIT" {
			commits++
		}
	}
	if rollbacks != 1 || commits != 0 {
		t.Errorf("rollback=%d commit=%d, want 1/0", rollbacks, commits)
	}
	if len(closed) != 1 {
		t.Errorf("closed physical connections = %d, want 1", len(closed))
	}
	if err := transaction.active(); !errors.Is(err, ErrTransactionDone) {
		t.Errorf("escaped transaction remains active: %v", err)
	}
}
