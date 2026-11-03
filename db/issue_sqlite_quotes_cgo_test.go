//go:build cgo

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type sqliteQuoteLogger struct {
	dbTestLogger
	sqlCalls []dbLogCall
}

func (logger *sqliteQuoteLogger) SqlCtx(message string, fields map[string]interface{}) {
	logger.sqlCalls = append(logger.sqlCalls, dbLogCall{msg: message, ctx: fields})
}

func newSQLiteQuoteDatabase(t *testing.T) (*DB, *SQLConnection) {
	t.Helper()
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	connection := NewSQLConnection(raw, &builder.Sqlite{})
	database := NewDB(connection)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := raw.Ping(); err != nil {
		t.Fatal(err)
	}
	return database, connection
}

func TestIssue125SQLiteSuccessAndFailureLogsRedactLiteral(t *testing.T) {
	database, _ := newSQLiteQuoteDatabase(t)
	logger := &sqliteQuoteLogger{}
	database.SetLogger(logger)
	database.queryLogging = true
	const successSQL = `SELECT '\' AS slash, 'AUDIT_CANARY' AS marker -- '`
	rows, err := database.Query(successSQL)
	if err != nil || len(rows) != 1 || rows[0]["slash"] != `\` || rows[0]["marker"] != "AUDIT_CANARY" {
		t.Fatalf("SQLite must execute the exact regression query: rows=%v err=%v", rows, err)
	}
	const failureSQL = `SELECT '\' AS slash, 'AUDIT_CANARY' AS marker FROM missing_issue125 -- '`
	if _, err := database.Query(failureSQL); err == nil || errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("failure regression must reach SQLite: %v", err)
	}
	if len(logger.sqlCalls) != 2 || len(logger.errorCalls) != 1 {
		t.Fatalf("expected success/failure SQL diagnostics and one execution error: sql=%d error=%d", len(logger.sqlCalls), len(logger.errorCalls))
	}
	for _, call := range append(logger.sqlCalls, logger.errorCalls...) {
		loggedSQL, ok := call.ctx["sql"].(string)
		if !ok || strings.Count(loggedSQL, sqlRedactionMarker) < 2 {
			t.Fatalf("missing SQL redaction evidence: %#v", call.ctx)
		}
		if dumped := fmt.Sprintf("%s %#v", call.msg, call.ctx); strings.Contains(dumped, "AUDIT_CANARY") {
			t.Fatalf("SQLite diagnostic leaked the literal: %s", dumped)
		}
	}
}

func TestIssue125SQLiteRawExecutorsBindAfterBackslash(t *testing.T) {
	database, connection := newSQLiteQuoteDatabase(t)
	ctx := context.Background()
	if _, err := database.Execute(`CREATE TABLE issue125 (id INTEGER PRIMARY KEY, slash TEXT, bound TEXT)`); err != nil {
		t.Fatal(err)
	}
	exercise := func(t *testing.T, executor ContextualRawQueryable) {
		t.Helper()
		rows, err := executor.QueryContext(ctx, `SELECT '\' AS slash, ? AS bound`, "BOUND_DUMMY")
		if err != nil || len(rows) != 1 || rows[0]["slash"] != `\` || rows[0]["bound"] != "BOUND_DUMMY" {
			t.Fatalf("query binding after backslash: %v %v", rows, err)
		}
		if changed, err := executor.ExecuteContext(ctx, `INSERT INTO issue125 (slash, bound) VALUES ('\', ?)`, "BOUND_DUMMY"); err != nil || changed != 1 {
			t.Fatalf("execute binding after backslash: %d %v", changed, err)
		}
	}
	t.Run("database", func(t *testing.T) { exercise(t, database) })
	t.Run("connection", func(t *testing.T) { exercise(t, connection) })
	t.Run("transaction", func(t *testing.T) {
		transaction, err := database.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = transaction.Rollback() }()
		exercise(t, transaction)
	})
	t.Run("pinned", func(t *testing.T) {
		err := database.WithPinnedSQLConnection(ctx, func(pinned PinnedSQLConnection) error {
			exercise(t, pinned)
			return pinned.TransactionContext(ctx, func(transaction ContextualRawQueryable) error {
				exercise(t, transaction)
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	seen := 0
	if err := connection.QueryEachContext(ctx, `SELECT '\' AS slash, ? AS bound`, func(row map[string]interface{}) bool {
		seen++
		if row["slash"] != `\` || row["bound"] != "BOUND_DUMMY" {
			t.Fatalf("streaming binding after backslash: %v", row)
		}
		return true
	}, "BOUND_DUMMY"); err != nil || seen != 1 {
		t.Fatalf("streaming query did not consume its binding: seen=%d err=%v", seen, err)
	}
	query := database.Table("issue125").Field("id").WhereRaw(`'\' = ? AND id = ?`, `\`, int64(1))
	if rows, err := query.Select(); err != nil || len(rows) != 1 || rows[0]["id"] != int64(1) {
		t.Fatalf("WhereRaw did not retain SQLite quote boundaries: %v %v", rows, err)
	}
	if rows, err := query.Group("id").HavingRaw(`'\' = ? AND COUNT(*) = ?`, `\`, int64(1)).Select(); err != nil || len(rows) != 1 {
		t.Fatalf("HavingRaw did not retain SQLite quote boundaries: %v %v", rows, err)
	}
	if count, err := query.Update(map[string]interface{}{"bound": "updated"}); err != nil || count != 1 {
		t.Fatalf("raw update predicate validation: %d %v", count, err)
	}
	if count, err := query.Delete(); err != nil || count != 1 {
		t.Fatalf("raw delete predicate validation: %d %v", count, err)
	}
}
