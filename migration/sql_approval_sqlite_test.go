//go:build cgo

package migration

import (
	"context"
	"errors"
	"testing"
)

func TestSQLApprovalRealSQLiteFirstApplyAndRollback(t *testing.T) {
	database := newSQLiteMigrationDatabase(t)
	if _, err := database.Execute("CREATE TABLE protected_records (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Execute("INSERT INTO protected_records (id) VALUES (7)"); err != nil {
		t.Fatal(err)
	}
	approvals := map[string]string{approvalName: approvalChecksum}
	tampered, err := NewSQLWithApproval(approvalName, []string{approvalCreate, "DELETE FROM protected_records"}, []string{approvalDrop}, approvals)
	if tampered != nil || !errors.Is(err, ErrMigrationNotApproved) {
		t.Fatalf("tampered migration=%v err=%v", tampered, err)
	}
	// No runner or history schema was needed for the first-use rejection.
	rows, err := database.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil || len(rows) != 1 || rows[0]["name"] != "protected_records" {
		t.Fatalf("rejected plan changed schema: %v %v", rows, err)
	}
	current, err := NewSQLWithApproval(approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, approvals)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(current); err != nil {
		t.Fatal(err)
	}
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Apply(context.Background())
	if err != nil || len(result.Names) != 1 {
		t.Fatalf("apply: %v %v", result, err)
	}
	rows, err = database.Query("SELECT id FROM approved_items")
	if err != nil || len(rows) != 1 || rows[0]["id"] != int64(51) {
		t.Fatalf("approved data: %v %v", rows, err)
	}
	history, err := store.Applied(context.Background())
	if err != nil || len(history) != 1 || history[0].Checksum != approvalChecksum {
		t.Fatalf("history: %v %v", history, err)
	}
	result, err = runner.Rollback(context.Background(), 1)
	if err != nil || len(result.Names) != 1 {
		t.Fatalf("rollback: %v %v", result, err)
	}
	rows, err = database.Query("SELECT name FROM sqlite_master WHERE type='table' AND name='approved_items'")
	if err != nil || len(rows) != 0 {
		t.Fatalf("rollback left table: %v %v", rows, err)
	}
	history, err = store.Applied(context.Background())
	if err != nil || len(history) != 0 {
		t.Fatalf("rollback left history: %v %v", history, err)
	}
	rows, err = database.Query("SELECT id FROM protected_records")
	if err != nil || len(rows) != 1 || rows[0]["id"] != int64(7) {
		t.Fatalf("protected data changed: %v %v", rows, err)
	}
}
