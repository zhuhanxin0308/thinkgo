package db

import (
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestIssue125SQLiteQuoteBoundaries(t *testing.T) {
	for _, sqlText := range []string{
		`SELECT '\', 'AUDIT_CANARY' -- '`,
		`SELECT 'first''part\', 'AUDIT_CANARY'`,
		`SELECT "\", 'AUDIT_CANARY'`,
	} {
		if got := redactSQLText(sqlText, "sqlite"); strings.Contains(got, "AUDIT_CANARY") || strings.Count(got, sqlRedactionMarker) < 2 {
			t.Fatalf("SQLite literal leaked after a backslash: %q", got)
		}
	}
	for _, sqlText := range []string{
		`SELECT '\', ? AS bound, '?' AS literal`,
		`SELECT 'first''part\', ? AS bound`,
		`SELECT "\", ? AS bound`,
	} {
		if count, err := countSQLPlaceholders(sqlText, " SQLite "); err != nil || count != 1 {
			t.Fatalf("SQLite placeholder after backslash quote: %q count=%d err=%v", sqlText, count, err)
		}
		if err := validateRawStatement(sqlText, 1, "sqlite"); err != nil {
			t.Fatal(err)
		}
		if err := validateRawStatement(sqlText, 0, "sqlite"); err == nil {
			t.Fatalf("missing SQLite binding accepted: %q", sqlText)
		}
	}
	for _, dialect := range []string{"", "mysql", "postgres", "sqlserver", "oracle"} {
		const sqlText = `SELECT 'left\'AUDIT_CANARY', ? AS bound`
		if count, err := countSQLPlaceholders(sqlText, dialect); err != nil || count != 1 {
			t.Fatalf("existing %q escape policy changed: %d %v", dialect, count, err)
		}
		if got := redactSQLText(sqlText, dialect); strings.Contains(got, "AUDIT_CANARY") {
			t.Fatalf("existing %q literal redaction changed: %q", dialect, got)
		}
	}
	var typedNil *builder.Sqlite
	if got := sqlBuilderDialect(typedNil); got != "" {
		t.Fatalf("typed nil builder exposed a dialect: %q", got)
	}
}
