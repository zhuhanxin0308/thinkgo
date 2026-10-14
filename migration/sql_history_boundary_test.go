package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const historyBoundaryName = "20261014030000_checksum_boundary"

// This reference is the pre-fix encoding, never a production fallback.
func legacySQLHistoryChecksum(up, down []string) string {
	var encoded strings.Builder
	encoded.WriteString("up\x00")
	for _, statement := range up {
		encoded.WriteString(strings.TrimSpace(statement))
		encoded.WriteByte(0)
	}
	encoded.WriteString("down\x00")
	for _, statement := range down {
		encoded.WriteString(strings.TrimSpace(statement))
		encoded.WriteByte(0)
	}
	sum := sha256.Sum256([]byte(encoded.String()))
	return hex.EncodeToString(sum[:])
}

func mustHistorySQL(t *testing.T, up, down []string) Migration {
	t.Helper()
	current, err := NewSQL(historyBoundaryName, up, down)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func historyBoundaryPlans(t *testing.T) (Migration, Migration) {
	t.Helper()
	return mustHistorySQL(t, []string{"SELECT 1"}, []string{"SELECT 2", "down", "SELECT 3"}),
		mustHistorySQL(t, []string{"SELECT 1", "down", "SELECT 2"}, []string{"SELECT 3"})
}

func TestSQLHistoryBoundarySeparatesDirections(t *testing.T) {
	a, b := historyBoundaryPlans(t)
	const old = "ca56f94a584554f62fb344000bf0b9893068213f71d0326891ee78ee58db587c"
	if legacySQLHistoryChecksum([]string{"SELECT 1"}, []string{"SELECT 2", "down", "SELECT 3"}) != old ||
		legacySQLHistoryChecksum([]string{"SELECT 1", "down", "SELECT 2"}, []string{"SELECT 3"}) != old {
		t.Fatal("legacy collision fixture changed")
	}
	if a.Checksum() == b.Checksum() || a.Checksum() == old || b.Checksum() == old {
		t.Fatal("history still aliases different Up/Down definitions or trusts the old digest")
	}
	// Do not reject or rewrite a complete procedure-call statement to hide the bug.
	for _, current := range []struct {
		migration Migration
		up, down  []string
	}{
		{a, []string{"SELECT 1"}, []string{"SELECT 2", "down", "SELECT 3"}},
		{b, []string{"SELECT 1", "down", "SELECT 2"}, []string{"SELECT 3"}},
	} {
		executor := &historyBoundaryStore{}
		if err := current.migration.Up(context.Background(), executor); err != nil || !reflect.DeepEqual(executor.statements, current.up) {
			t.Fatalf("Up execution changed: %q %v", executor.statements, err)
		}
		executor.statements = nil
		if err := current.migration.Down(context.Background(), executor); err != nil || !reflect.DeepEqual(executor.statements, current.down) {
			t.Fatalf("Down execution changed: %q %v", executor.statements, err)
		}
	}
}

func TestSQLHistoryBoundaryPreservesUnambiguousHistory(t *testing.T) {
	for _, statement := range []string{"SELECT 1", "up", "DOWN", "Down", "down;", "EXEC down", "SELECT 'down'", "-- down\nSELECT 1", "SELECT '\xff'", "SELECT '中文'"} {
		up, down := []string{"\u2003" + statement + "\n", "SELECT 2"}, []string{"SELECT 3"}
		current := mustHistorySQL(t, up, down)
		if current.Checksum() != legacySQLHistoryChecksum(up, down) {
			t.Fatalf("unambiguous legacy history changed for %q", statement)
		}
	}
	for _, down := range [][]string{nil, {}} {
		current := mustHistorySQL(t, []string{"SELECT 1"}, down)
		if current.Checksum() != legacySQLHistoryChecksum([]string{"SELECT 1"}, nil) {
			t.Fatal("nil/empty rollback history changed")
		}
		if err := current.Down(context.Background(), &historyBoundaryStore{}); !errors.Is(err, ErrIrreversibleMigration) {
			t.Fatalf("irreversible contract changed: %v", err)
		}
	}
}

func TestSQLHistoryBoundarySnapshotAndErrors(t *testing.T) {
	up, down := []string{"\u2003down\n", "SELECT 2"}, []string{"SELECT 3"}
	current := mustHistorySQL(t, up, down)
	want := mustHistorySQL(t, []string{"down", "SELECT 2"}, down).Checksum()
	up[0], down[0] = "SELECT 999", "SELECT 999"
	if current.Checksum() != want || !migrationChecksumPattern.MatchString(want) {
		t.Fatal("normalization, ownership, or public checksum format changed")
	}
	executor := &historyBoundaryStore{}
	if err := current.Up(context.Background(), executor); err != nil || !reflect.DeepEqual(executor.statements, []string{"down", "SELECT 2"}) {
		t.Fatalf("snapshot execution: %q %v", executor.statements, err)
	}
	failure := errors.New("execution failure")
	executor.failure = failure
	if err := current.Down(context.Background(), executor); !errors.Is(err, failure) {
		t.Fatalf("execution error identity changed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := current.Up(ctx, executor); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation changed: %v", err)
	}
	for _, invalid := range []struct{ up, down []string }{
		{nil, []string{"down"}}, {[]string{"\x00down"}, nil},
		{[]string{"down"}, []string{" "}}, {[]string{"down"}, []string{"SELECT\x00"}},
	} {
		if current, err := NewSQL(historyBoundaryName, invalid.up, invalid.down); current != nil || !errors.Is(err, ErrInvalidMigration) {
			t.Fatalf("invalid SQL accepted: %v", err)
		}
	}
}

// Store/Executor double for Runner policy, not a SQL interpreter or durable store.
type historyBoundaryStore struct {
	history    []History
	journal    []JournalEntry
	statements []string
	mutations  int
	failure    error
}

func (s *historyBoundaryStore) Ensure(context.Context) error { return nil }
func (s *historyBoundaryStore) Inspect(context.Context) ([]History, bool, error) {
	return append([]History(nil), s.history...), true, nil
}
func (s *historyBoundaryStore) Applied(context.Context) ([]History, error) {
	return append([]History(nil), s.history...), nil
}
func (s *historyBoundaryStore) Apply(ctx context.Context, m Migration, _ int64) error {
	s.mutations++
	return m.Up(ctx, s)
}
func (s *historyBoundaryStore) Revert(ctx context.Context, m Migration) error {
	s.mutations++
	return m.Down(ctx, s)
}
func (s *historyBoundaryStore) Journal(context.Context) ([]JournalEntry, error) {
	return append([]JournalEntry(nil), s.journal...), nil
}
func (s *historyBoundaryStore) ResolveDirty(context.Context, string, RecoveryResolution) error {
	s.mutations++
	s.journal[0].State, s.journal[0].Operation = JournalApplied, JournalOperationApply
	return nil
}
func (s *historyBoundaryStore) QueryContext(context.Context, string, ...interface{}) ([]map[string]interface{}, error) {
	return nil, errors.New("unexpected query")
}
func (s *historyBoundaryStore) ExecuteContext(ctx context.Context, statement string, _ ...interface{}) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.failure != nil {
		return 0, s.failure
	}
	s.statements = append(s.statements, statement)
	return 1, nil
}

func newHistoryBoundaryRunner(t *testing.T, current Migration, store Store) *Runner {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register(current); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(registry, store)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func runHistoryBoundaryOperation(ctx context.Context, runner *Runner, operation string) error {
	switch operation {
	case "Status":
		_, err := runner.Status(ctx)
		return err
	case "InspectStatus":
		_, _, err := runner.InspectStatus(ctx)
		return err
	case "Apply":
		_, err := runner.Apply(ctx)
		return err
	case "Rollback":
		_, err := runner.Rollback(ctx, 1)
		return err
	case "ResolveDirty":
		return runner.ResolveDirty(ctx, historyBoundaryName, RecoveryMarkApplied)
	default:
		return errors.New("unknown test operation")
	}
}

func TestSQLHistoryBoundaryRunnerRejectsDriftWithoutFallback(t *testing.T) {
	a, b := historyBoundaryPlans(t)
	const legacy = "ca56f94a584554f62fb344000bf0b9893068213f71d0326891ee78ee58db587c"
	for _, sample := range []struct {
		name, stored string
		current      Migration
	}{
		{"new_history_direction_change", a.Checksum(), b},
		{"legacy_same_definition", legacy, a},
		{"legacy_alternate_definition", legacy, b},
	} {
		for _, operation := range []string{"Status", "InspectStatus", "Apply", "Rollback", "ResolveDirty"} {
			t.Run(sample.name+"/"+operation, func(t *testing.T) {
				store := &historyBoundaryStore{
					history: []History{{Name: historyBoundaryName, Checksum: sample.stored, Batch: 1}},
					journal: []JournalEntry{{Name: historyBoundaryName, Checksum: sample.stored, Batch: 1,
						Operation: JournalOperationApply, State: JournalApplied, FencingToken: 1}},
				}
				if operation == "ResolveDirty" {
					store.journal[0].State = JournalFailed
					// Exercise the independent recovery-target checksum check, without applied history.
					store.history = nil
				}
				history, journal := append([]History(nil), store.history...), append([]JournalEntry(nil), store.journal...)
				err := runHistoryBoundaryOperation(context.Background(), newHistoryBoundaryRunner(t, sample.current, store), operation)
				if !errors.Is(err, ErrMigrationDrift) {
					t.Fatalf("direction/legacy drift accepted: %v", err)
				}
				if store.mutations != 0 || len(store.statements) != 0 || !reflect.DeepEqual(store.history, history) || !reflect.DeepEqual(store.journal, journal) {
					t.Fatal("drift check executed SQL or rewrote history/journal")
				}
			})
		}
	}
}

func TestSQLHistoryBoundaryConcurrentConstruction(t *testing.T) {
	a, _ := historyBoundaryPlans(t)
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			current, err := NewSQL(historyBoundaryName, []string{"SELECT 1"}, []string{"SELECT 2", "down", "SELECT 3"})
			if err != nil || current == nil || current.Checksum() != a.Checksum() {
				t.Errorf("concurrent checksum instability: %v", err)
			}
		}()
	}
	group.Wait()
}

func FuzzSQLHistoryDirectionBoundary(f *testing.F) {
	for _, seed := range []string{"SELECT 1", "down", "up", " SELECT 1\n", "SELECT '\xff'", "SELECT '中文'"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, statement string) {
		if len(statement) > 4096 || strings.TrimSpace(statement) == "" || strings.ContainsRune(statement, 0) {
			return
		}
		a := mustHistorySQL(t, []string{statement}, []string{"down"})
		b := mustHistorySQL(t, []string{statement, "down"}, nil)
		if a.Checksum() == b.Checksum() {
			t.Fatal("rollback-only statement moved into Up without history drift")
		}
		old := legacySQLHistoryChecksum([]string{statement}, []string{"down"})
		if a.Checksum() == old || b.Checksum() == old {
			t.Fatal("ambiguous legacy digest reused")
		}
	})
}
