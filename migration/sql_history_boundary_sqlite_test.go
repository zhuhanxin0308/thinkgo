//go:build cgo

package migration

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestSQLHistoryBoundaryRealSQLiteHistoryAndJournal(t *testing.T) {
	for _, scenario := range []string{"new_history_direction_change", "legacy_same_definition", "legacy_alternate_definition"} {
		for _, operation := range []string{"Status", "InspectStatus", "Apply", "Rollback", "ResolveDirty"} {
			t.Run(scenario+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				database := newSQLiteMigrationDatabase(t)
				upA := []string{"CREATE TABLE checksum_protected (id INTEGER PRIMARY KEY)", "INSERT INTO checksum_protected (id) VALUES (7)"}
				downA := []string{"SELECT 2", "down", "DELETE FROM checksum_protected"}
				upB := append(append([]string(nil), upA...), "down", "SELECT 2")
				downB := []string{"DELETE FROM checksum_protected"}
				a, b := mustHistorySQL(t, upA, downA), mustHistorySQL(t, upB, downB)
				legacy := legacySQLHistoryChecksum(upA, downA)
				if legacy != legacySQLHistoryChecksum(upB, downB) {
					t.Fatal("legacy boundary fixture changed")
				}
				writer := a
				if scenario != "new_history_direction_change" {
					// Emulate the old writer via the public programmatic constructor.
					// Real A.Up executes; real DatabaseStore persists the legacy history and journal.
					var err error
					writer, err = New(historyBoundaryName, legacy, a.Up, a.Down)
					if err != nil {
						t.Fatal(err)
					}
				}
				store, err := NewDatabaseStore(database)
				if err != nil {
					t.Fatal(err)
				}
				result, err := newHistoryBoundaryRunner(t, writer, store).Apply(ctx)
				if err != nil || len(result.Names) != 1 {
					t.Fatalf("persist initial history: %#v %v", result, err)
				}
				initial, err := store.Applied(ctx)
				if err != nil || len(initial) != 1 || initial[0].Checksum != writer.Checksum() {
					t.Fatalf("persisted checksum: %#v %v", initial, err)
				}
				if operation == "ResolveDirty" {
					// A controlled failed-journal fixture without applied history reaches
					// validateRecoveryTarget, rather than failing only in validateHistory.
					if affected, err := database.Execute("DELETE FROM "+migrationHistoryTable+" WHERE name = ?", historyBoundaryName); err != nil || affected != 1 {
						t.Fatalf("dirty history fixture: %d %v", affected, err)
					}
					if affected, err := database.Execute("UPDATE "+migrationJournalTable+" SET state = ?, operation = ? WHERE name = ?", string(JournalFailed), string(JournalOperationApply), historyBoundaryName); err != nil || affected != 1 {
						t.Fatalf("dirty journal fixture: %d %v", affected, err)
					}
				}
				historyBefore, err := store.Applied(ctx)
				if err != nil {
					t.Fatal(err)
				}
				journalBefore, err := store.Journal(ctx)
				if err != nil || len(journalBefore) != 1 || journalBefore[0].Checksum != writer.Checksum() {
					t.Fatalf("journal checksum: %#v %v", journalBefore, err)
				}
				current := b
				if scenario == "legacy_same_definition" {
					current = a
				}
				err = runHistoryBoundaryOperation(ctx, newHistoryBoundaryRunner(t, current, store), operation)
				if !errors.Is(err, ErrMigrationDrift) {
					t.Fatalf("unsafe persisted history accepted: %v", err)
				}
				historyAfter, err := store.Applied(ctx)
				if err != nil || !reflect.DeepEqual(historyBefore, historyAfter) {
					t.Fatalf("history rewritten: %#v %v", historyAfter, err)
				}
				journalAfter, err := store.Journal(ctx)
				if err != nil || !reflect.DeepEqual(journalBefore, journalAfter) {
					t.Fatalf("journal rewritten: %#v %v", journalAfter, err)
				}
				rows, err := database.Query("SELECT id FROM checksum_protected")
				if err != nil || len(rows) != 1 || rows[0]["id"] != int64(7) {
					t.Fatalf("altered rollback reached protected data: %#v %v", rows, err)
				}
				// A.Down's exact "down" is deliberately never sent to SQLite.
				// This proves history/journal enforcement, not SQL Server procedure execution.
			})
		}
	}
}

func TestSQLHistoryBoundaryRealSQLiteUnambiguousUpgradeAndRollback(t *testing.T) {
	ctx := context.Background()
	database := newSQLiteMigrationDatabase(t)
	up := []string{"CREATE TABLE checksum_ordinary (id INTEGER PRIMARY KEY)", "INSERT INTO checksum_ordinary (id) VALUES (7)"}
	down := []string{"DROP TABLE checksum_ordinary"}
	current := mustHistorySQL(t, up, down)
	legacy := legacySQLHistoryChecksum(up, down)
	if current.Checksum() != legacy {
		t.Fatal("ordinary history unnecessarily changed")
	}
	oldWriter, err := New(historyBoundaryName, legacy, current.Up, current.Down)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDatabaseStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := newHistoryBoundaryRunner(t, oldWriter, store).Apply(ctx); err != nil || len(result.Names) != 1 {
		t.Fatalf("old writer apply: %#v %v", result, err)
	}
	upgraded := newHistoryBoundaryRunner(t, current, store)
	statuses, err := upgraded.Status(ctx)
	if err != nil || len(statuses) != 1 || !statuses[0].Applied {
		t.Fatalf("upgrade status: %#v %v", statuses, err)
	}
	if result, err := upgraded.Apply(ctx); err != nil || len(result.Names) != 0 {
		t.Fatalf("upgrade applied twice: %#v %v", result, err)
	}
	if result, err := upgraded.Rollback(ctx, 1); err != nil || len(result.Names) != 1 {
		t.Fatalf("upgrade rollback: %#v %v", result, err)
	}
	history, err := store.Applied(ctx)
	if err != nil || len(history) != 0 {
		t.Fatalf("rollback history: %#v %v", history, err)
	}
	journal, err := store.Journal(ctx)
	if err != nil || len(journal) != 1 || journal[0].State != JournalReverted || journal[0].Checksum != legacy {
		t.Fatalf("rollback journal: %#v %v", journal, err)
	}
	rows, err := database.Query("SELECT name FROM sqlite_master WHERE type='table' AND name='checksum_ordinary'")
	if err != nil || len(rows) != 0 {
		t.Fatalf("rollback schema: %#v %v", rows, err)
	}
	// A rolled-back application binary can still read the unchanged ordinary history.
	if _, err := newHistoryBoundaryRunner(t, oldWriter, store).Status(ctx); err != nil {
		t.Fatalf("old reader: %v", err)
	}
}
