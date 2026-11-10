package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPostgresArrayLiteralsRemainRedactedInQueryLogs(t *testing.T) {
	database, _ := newPostgresArrayValidationDatabase(t)
	logger := &queryDiagnosticLogger{}
	database.SetLogger(logger)
	database.queryLogging = true
	for _, query := range []string{
		`SELECT ARRAY['\]PRIVATE_MARKER'], ?`,
		`SELECT ARRAY['[PRIVATE_MARKER]', 'it''s ]PRIVATE_MARKER'], ?`,
		`SELECT ARRAY[ARRAY[E'\\]PRIVATE_MARKER']], ?`,
		`SELECT ARRAY[$tag$]PRIVATE_MARKER$tag$ /* ]PRIVATE_MARKER */], ?`,
		`SELECT ARRAY['\]PRIVATE_MARKER'][1], ?`,
	} {
		redacted := redactSQLLiteralsAndComments(query, "postgres")
		if strings.Contains(redacted, "PRIVATE_MARKER") || !strings.HasSuffix(redacted, ", ?") {
			t.Errorf("数组脱敏泄漏内容或吞掉 SQL 结构: %q", redacted)
		}
		if _, err := database.QueryContext(context.Background(), query, 1); err != nil {
			t.Fatal(err)
		}
	}
	if calls := logger.calls(); len(calls) != 5 || strings.Contains(fmt.Sprint(calls), "PRIVATE_MARKER") {
		t.Fatalf("实际查询日志泄漏数组值: %+v", calls)
	}
	if got := redactSQLLiteralsAndComments(`SELECT [valid]]identifier], 'PRIVATE_MARKER'`, "sqlserver"); got != `SELECT [valid]]identifier], '[REDACTED]'` {
		t.Fatalf("SQL Server 引用标识符契约回归: %q", got)
	}
}

func FuzzPostgresArrayRedaction(f *testing.F) {
	for _, seed := range []string{`]`, `\]`, `['x']`, "\n", "$tag$", "文本"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 512 || strings.ContainsRune(value, 0) {
			return
		}
		for _, prefix := range []string{"", "E"} {
			literal := "synthetic-private" + value
			if prefix == "E" {
				literal = strings.ReplaceAll(literal, `\`, `\\`)
			}
			literal = strings.ReplaceAll(literal, "'", "''")
			query := "SELECT ARRAY[" + prefix + "'" + literal + "'], ?"
			want := "SELECT ARRAY[" + prefix + "'[REDACTED]'], ?"
			if got := redactSQLLiteralsAndComments(query, "postgres"); got != want {
				t.Fatalf("数组字符串边界泄漏: %q", got)
			}
		}
	})
}
