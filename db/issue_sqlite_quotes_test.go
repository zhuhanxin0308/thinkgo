package db

import (
	"context"
	"errors"
	"reflect"
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

func TestIssue125RawValidationDialectPreservesPredicateBuilder(t *testing.T) {
	for _, b := range []Builder{nil, &builder.Mysql{}, &builder.Pgsql{}, &builder.Sqlite{}, &builder.Sqlsrv{}} {
		name := sqlBuilderDialect(b)
		if name == "" {
			name = "unquoted"
		}
		t.Run(name, func(t *testing.T) {
			predicate := newPredicate().appendValidated("id = ?", []interface{}{7}).appendRaw(`'\' = ?`, []interface{}{`\`})
			predicate.builder = b
			where, args, err := predicate.compileSQLWithDialect("sqlite")
			field := "id"
			if b != nil {
				field = b.QuoteIdentifier(field)
			}
			if err != nil || !reflect.DeepEqual(where, []string{field + " = ?", `'\' = ?`}) || !reflect.DeepEqual(args, []interface{}{7, `\`}) {
				t.Fatalf("execution dialect changed predicate rendering or bindings: %q %#v %v", where, args, err)
			}
			if _, _, err := predicate.compileSQLWithDialect("mysql"); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("non-SQLite raw validation policy changed: %v", err)
			}
		})
	}
}

func TestIssue125SQLiteConnectionValidatesNestedRawWithoutChangingRendering(t *testing.T) {
	predicate := Predicate{nodes: []PredicateNode{{
		Kind:     PredicateBoolean,
		Operator: "AND",
		Children: []PredicateNode{
			comparisonNode("id", "=", 7),
			{
				Kind:     PredicateBoolean,
				Operator: "OR",
				Children: []PredicateNode{
					{Kind: PredicateRaw, SQL: `'\' = ?`, Values: []interface{}{`\`}},
					comparisonNode("enabled", "=", true),
				},
			},
		},
	}}}
	ctx := context.Background()
	for _, test := range []struct {
		name string
		run  func(*SQLConnection, Predicate) error
	}{
		{"select", func(connection *SQLConnection, predicate Predicate) error {
			_, err := connection.Select(ctx, newSelectRequest("users", "value", predicate, "id", "", 1, 0, nil))
			return err
		}},
		{"update", func(connection *SQLConnection, predicate Predicate) error {
			_, err := connection.Update(ctx, newUpdateRequest("users", map[string]interface{}{"name": "Ada"}, predicate, "id"))
			return err
		}},
		{"delete", func(connection *SQLConnection, predicate Predicate) error {
			_, err := connection.Delete(ctx, newDeleteRequest("users", predicate, "id", false))
			return err
		}},
		{"count", func(connection *SQLConnection, predicate Predicate) error {
			_, err := connection.Count(ctx, newCountRequest("users", predicate, "id"))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, recorder := newRecordingHardeningSQLConnection(t)
			if err := test.run(connection, predicate); err != nil {
				t.Fatal(err)
			}
			if got := recorder.recordedQuery(); !strings.Contains(got, ` WHERE (id = ? AND ('\' = ? OR enabled = ?))`) {
				t.Fatalf("connection changed existing predicate rendering: %q", got)
			}
			invalid := predicate.clone()
			invalid.nodes[0].Children[1].Children[0].Values = nil
			if err := test.run(connection, invalid); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("nested raw clause accepted missing SQLite binding: %v", err)
			}
		})
	}
}
