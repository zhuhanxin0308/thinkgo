package db

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

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
