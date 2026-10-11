package migration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const (
	approvalName = "202610120001_reviewed_table"
	// Fixed review fixture; independently computed from the documented encoding.
	approvalChecksum = "048a4555502260d95723a632a30b9d90efa2fd8f63dfa60b0244825e3299c03e"
	approvalCreate   = "CREATE TABLE approved_items (id INTEGER PRIMARY KEY)"
	approvalInsert   = "INSERT INTO approved_items (id) VALUES (51)"
	approvalDrop     = "DROP TABLE approved_items"
)

func TestSQLApprovalRejectsUnapprovedDefinitions(t *testing.T) {
	for _, sample := range []struct {
		name          string
		migrationName string
		up, down      []string
		approvals     map[string]string
		want          error
	}{
		{"missing", approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, nil, ErrMigrationNotApproved},
		{"empty", approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, map[string]string{approvalName: ""}, ErrMigrationNotApproved},
		{"wrong_checksum", approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, map[string]string{approvalName: strings.Repeat("0", 64)}, ErrMigrationNotApproved},
		{"uppercase_checksum", approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, map[string]string{approvalName: strings.ToUpper(approvalChecksum)}, ErrMigrationNotApproved},
		{"other_name", "202610120002_other_table", []string{approvalCreate, approvalInsert}, []string{approvalDrop}, map[string]string{approvalName: approvalChecksum}, ErrMigrationNotApproved},
		{"changed_up", approvalName, []string{approvalCreate, "DROP TABLE protected_records"}, []string{approvalDrop}, map[string]string{approvalName: approvalChecksum}, ErrMigrationNotApproved},
		{"changed_down", approvalName, []string{approvalCreate, approvalInsert}, []string{"DROP TABLE protected_records"}, map[string]string{approvalName: approvalChecksum}, ErrMigrationNotApproved},
		{"reordered_up", approvalName, []string{approvalInsert, approvalCreate}, []string{approvalDrop}, map[string]string{approvalName: approvalChecksum}, ErrMigrationNotApproved},
		{"joined_up", approvalName, []string{approvalCreate + "; " + approvalInsert}, []string{approvalDrop}, map[string]string{approvalName: approvalChecksum}, ErrMigrationNotApproved},
		{"removed_down", approvalName, []string{approvalCreate, approvalInsert}, nil, map[string]string{approvalName: approvalChecksum}, ErrMigrationNotApproved},
		{"invalid_name", "invalid", []string{approvalCreate}, nil, nil, ErrInvalidMigration},
		{"empty_up", approvalName, nil, nil, nil, ErrInvalidMigration},
		{"nul_statement", approvalName, []string{"SELECT\x00private"}, nil, nil, ErrInvalidMigration},
	} {
		t.Run(sample.name, func(t *testing.T) {
			current, err := NewSQLWithApproval(sample.migrationName, sample.up, sample.down, sample.approvals)
			if current != nil || !errors.Is(err, sample.want) {
				t.Fatalf("migration=%v error=%v want=%v", current, err, sample.want)
			}
			if strings.Contains(err.Error(), "protected_records") || strings.Contains(err.Error(), "private") {
				t.Fatal("approval error exposed SQL text")
			}
		})
	}
}

type approvalExecutor struct {
	statements []string
	failure    error
}

func (e *approvalExecutor) QueryContext(context.Context, string, ...interface{}) ([]map[string]interface{}, error) {
	return nil, errors.New("unexpected query")
}
func (e *approvalExecutor) ExecuteContext(ctx context.Context, statement string, _ ...interface{}) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if e.failure != nil {
		return 0, e.failure
	}
	e.statements = append(e.statements, statement)
	return 1, nil
}

func TestSQLApprovalSnapshotsApprovedDefinition(t *testing.T) {
	up := []string{"  " + approvalCreate + "\n", approvalInsert}
	down := []string{approvalDrop}
	approvals := map[string]string{approvalName: approvalChecksum}
	current, err := NewSQLWithApproval(approvalName, up, down, approvals)
	if err != nil {
		t.Fatal(err)
	}
	up[0], down[0] = "DROP DATABASE private", "DROP TABLE private"
	delete(approvals, approvalName)
	executor := &approvalExecutor{}
	if err := current.Up(context.Background(), executor); err != nil {
		t.Fatal(err)
	}
	if err := current.Down(context.Background(), executor); err != nil {
		t.Fatal(err)
	}
	if current.Name() != approvalName || current.Checksum() != approvalChecksum ||
		!reflect.DeepEqual(executor.statements, []string{approvalCreate, approvalInsert, approvalDrop}) {
		t.Fatalf("approved snapshot changed: %v", executor.statements)
	}
}

func TestSQLApprovalPreservesExecutionErrors(t *testing.T) {
	current, err := NewSQLWithApproval(approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, map[string]string{approvalName: approvalChecksum})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("backend failure")
	executor := &approvalExecutor{failure: failure}
	if err := current.Up(context.Background(), executor); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	executor.failure = nil
	if err := current.Up(canceled, executor); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(executor.statements) != 0 {
		t.Fatal("failed execution reached statements")
	}
	var missing *approvalExecutor
	if err := current.Up(context.Background(), missing); !errors.Is(err, ErrInvalidMigration) {
		t.Fatal(err)
	}
	irreversible, err := NewSQL(approvalName, []string{approvalCreate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A separate reviewed irreversible definition is legal, but still cannot roll back.
	approved, err := NewSQLWithApproval(approvalName, []string{approvalCreate}, nil, map[string]string{approvalName: irreversible.Checksum()})
	if err != nil {
		t.Fatal(err)
	}
	if err := approved.Down(context.Background(), executor); !errors.Is(err, ErrIrreversibleMigration) {
		t.Fatal(err)
	}
}

func TestSQLApprovalConcurrentConstruction(t *testing.T) {
	approvals := map[string]string{approvalName: approvalChecksum}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			current, err := NewSQLWithApproval(approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, approvals)
			if err != nil {
				t.Error(err)
				return
			}
			executor := &approvalExecutor{}
			if err := current.Up(context.Background(), executor); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(executor.statements, []string{approvalCreate, approvalInsert}) {
				t.Error("unexpected SQL")
			}
		})
	}
	wg.Wait()
}

func FuzzSQLApprovalPinnedContent(f *testing.F) {
	f.Add(approvalCreate, approvalInsert, approvalDrop)
	f.Add(approvalCreate, "DROP TABLE protected_records", approvalDrop)
	f.Add(" "+approvalCreate+"\n", approvalInsert, approvalDrop)
	f.Fuzz(func(t *testing.T, create, insert, drop string) {
		if len(create)+len(insert)+len(drop) > 8192 {
			t.Skip()
		}
		current, err := NewSQLWithApproval(approvalName, []string{create, insert}, []string{drop}, map[string]string{approvalName: approvalChecksum})
		canonical := strings.TrimSpace(create) == approvalCreate && strings.TrimSpace(insert) == approvalInsert && strings.TrimSpace(drop) == approvalDrop
		if canonical {
			if err != nil || current == nil || current.Checksum() != approvalChecksum {
				t.Fatalf("reviewed definition rejected: %v", err)
			}
		} else if err == nil || current != nil {
			t.Fatal("changed SQL accepted against pinned review")
		}
	})
}
