package migration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSQLApprovalDigestEncodingVectors(t *testing.T) {
	for _, sample := range []struct {
		name     string
		up, down []string
		want     string
	}{
		{approvalName, []string{approvalCreate, approvalInsert}, []string{approvalDrop}, approvalDigest},
		{"202610120002_boundary", []string{"SELECT 1"}, []string{"down"}, "sql-approval-v1:8af4a5ba1a7f0bbdd82d31f064c2ce2948135a5b3e377c0de1c83b44ab165d63"},
		{"202610120002_boundary", []string{"SELECT 1", "down"}, nil, "sql-approval-v1:7adc16d94145370dda5f958c43c272e7c4f2fb0d34dfa0d4d82469eb239f6e46"},
		{approvalName, []string{"SELECT \xff"}, nil, "sql-approval-v1:51a1f9f88a69d848f278266b1df5768e46fd53592c59d59b700104ef63b917ff"},
		{approvalName, []string{"SELECT \xfe"}, nil, "sql-approval-v1:487f2750a9b6814e9816f8c303a623a46633f027fc258069f68232538ef8140f"},
	} {
		got, err := SQLApprovalDigest(sample.name, sample.up, sample.down)
		if err != nil || got != sample.want {
			t.Fatalf("digest=%q want=%q err=%v", got, sample.want, err)
		}
	}
}

func TestSQLApprovalRejectsDirectionBoundaryCollision(t *testing.T) {
	const name = "202610120002_boundary"
	plans := []struct{ up, down []string }{
		{[]string{"SELECT 1"}, []string{"down"}},
		{[]string{"SELECT 1", "down"}, nil},
	}
	definitions := make([]Migration, len(plans))
	digests := make([]string, len(plans))
	for i, plan := range plans {
		var err error
		definitions[i], err = NewSQL(name, plan.up, plan.down)
		if err != nil {
			t.Fatal(err)
		}
		digests[i], err = SQLApprovalDigest(name, plan.up, plan.down)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Preserve the old collision fixture, but both current history and approval must separate it.
	if legacySQLHistoryChecksum(plans[0].up, plans[0].down) != legacySQLHistoryChecksum(plans[1].up, plans[1].down) {
		t.Fatal("legacy collision fixture changed")
	}
	if definitions[0].Checksum() == definitions[1].Checksum() || digests[0] == digests[1] {
		t.Fatal("history or approval failed to separate directions")
	}
	for i, plan := range plans {
		approvals := map[string]string{name: digests[i]}
		approved, err := NewSQLWithApproval(name, plan.up, plan.down, approvals)
		if err != nil {
			t.Fatal(err)
		}
		executor := &approvalExecutor{}
		if err := approved.Up(context.Background(), executor); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(executor.statements, plan.up) || approved.Checksum() != definitions[i].Checksum() {
			t.Fatal("approved plan or history changed")
		}
		other := plans[1-i]
		rejected, err := NewSQLWithApproval(name, other.up, other.down, approvals)
		if rejected != nil || !errors.Is(err, ErrMigrationNotApproved) {
			t.Fatal("approval accepted SQL moved across the Up/Down boundary")
		}
		for _, history := range []string{definitions[i].Checksum(), legacySQLHistoryChecksum(plan.up, plan.down)} {
			if rejected, err := NewSQLWithApproval(name, plan.up, plan.down, map[string]string{name: history}); rejected != nil || !errors.Is(err, ErrMigrationNotApproved) {
				t.Fatal("history checksum became an authorization fallback")
			}
		}
	}
}

func TestSQLApprovalDigestBindsNameAndStructure(t *testing.T) {
	const name = "202610120003_structure"
	originalUp, originalDown := []string{"SELECT 1", "SELECT 2"}, []string{"down"}
	original, err := SQLApprovalDigest(name, originalUp, originalDown)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name     string
		up, down []string
	}{
		{"202610120004_other", originalUp, originalDown},
		{name, []string{"SELECT 2", "SELECT 1"}, originalDown},
		{name, []string{"SELECT 1; SELECT 2"}, originalDown},
		{name, []string{"SELECT 1", "SELECT 2", "down"}, nil},
		{name, originalUp, []string{"down", "down"}},
		{name, []string{"SELECT 1", "SELECT 2", ""}, originalDown},
	} {
		got, err := SQLApprovalDigest(sample.name, sample.up, sample.down)
		if err == nil && got == original {
			t.Fatal("distinct name or statement structure received the same approval")
		}
		// Even placing the original digest under the new name cannot rebind it.
		current, err := NewSQLWithApproval(sample.name, sample.up, sample.down, map[string]string{sample.name: original})
		if err == nil || current != nil {
			t.Fatal("different plan accepted against original approval")
		}
	}
	for _, down := range [][]string{nil, {}} {
		got, err := SQLApprovalDigest(name, []string{"\u2003SELECT 1\n"}, down)
		if err != nil {
			t.Fatal(err)
		}
		want, err := SQLApprovalDigest(name, []string{"SELECT 1"}, nil)
		if err != nil || got != want {
			t.Fatal("NewSQL normalization or empty Down semantics changed")
		}
	}
}

func TestSQLApprovalDigestPreservesValidation(t *testing.T) {
	for _, sample := range []struct {
		name     string
		up, down []string
	}{
		{"invalid", []string{"SELECT 1"}, nil},
		{approvalName, nil, nil},
		{approvalName, []string{" \n"}, nil},
		{approvalName, []string{"SELECT\x00private"}, nil},
		{approvalName, []string{"SELECT 1"}, []string{""}},
	} {
		digest, err := SQLApprovalDigest(sample.name, sample.up, sample.down)
		if digest != "" || !errors.Is(err, ErrInvalidMigration) {
			t.Fatalf("invalid definition: digest=%q error=%v", digest, err)
		}
	}
}

func FuzzSQLApprovalDirectionBoundary(f *testing.F) {
	for _, seed := range []string{"SELECT 1", "down", "up", " SELECT 1\n", "SELECT '\xff'", "SELECT '中文'"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, statement string) {
		if len(statement) > 4096 || strings.TrimSpace(statement) == "" || strings.ContainsRune(statement, 0) {
			t.Skip()
		}
		const name = "202610120005_fuzz_boundary"
		up, down := []string{statement}, []string{"down"}
		digest, err := SQLApprovalDigest(name, up, down)
		if err != nil {
			t.Fatal(err)
		}
		approvals := map[string]string{name: digest}
		if approved, err := NewSQLWithApproval(name, up, down, approvals); approved == nil || err != nil {
			t.Fatal("original plan rejected")
		}
		if changed, err := NewSQLWithApproval(name, []string{statement, "down"}, nil, approvals); changed != nil || !errors.Is(err, ErrMigrationNotApproved) {
			t.Fatal("rollback-only statement moved into Up under unchanged approval")
		}
	})
}
