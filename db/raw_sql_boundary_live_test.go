//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestLiveMySQLRawSQLBoundary(t *testing.T) {
	exerciseRawSQLBoundary(t, connectLiveSQL(t, "mysql"), liveSQLTableName())
}

func TestLivePostgreSQLRawSQLBoundary(t *testing.T) {
	database := connectLiveSQL(t, "pgsql")
	exerciseRawSQLBoundary(t, database, liveSQLTableName())
	t.Run("array_bindings", func(t *testing.T) {
		exercisePostgreSQLArrayBindings(t, database)
	})
}

func exercisePostgreSQLArrayBindings(t *testing.T, database *db.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	table := liveSQLTableName()
	if _, err := database.ExecuteContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, numbers INTEGER[] NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := database.ExecuteContext(cleanup, "DROP TABLE "+table); err != nil {
			t.Error(err)
		}
	})
	if affected, err := database.ExecuteContext(ctx, "INSERT INTO "+table+" (id, numbers) VALUES (?, ARRAY[?::integer, ?::integer])", 1, 11, 22); err != nil || affected != 1 {
		t.Fatalf("array insert: affected=%d err=%v", affected, err)
	}
	exercise := func(t *testing.T, executor db.ContextualRawQueryable) {
		t.Helper()
		for _, test := range []struct {
			name string
			sql  string
			args []interface{}
			want int64
		}{
			{"constructor and index", `SELECT (ARRAY[?::integer, ?::integer])[?] AS id`, []interface{}{11, 22, 2}, 22},
			{"nested", `SELECT (ARRAY[ARRAY[?::integer, ?::integer], ARRAY[?::integer, ?::integer]])[?][?] AS id`, []interface{}{11, 22, 33, 44, 2, 2}, 44},
			{"slice", `SELECT cardinality((ARRAY[?::integer, ?::integer, ?::integer])[?:?]) AS id`, []interface{}{11, 22, 33, 1, 2}, 2},
			{"quoted contents", `SELECT ?::integer AS id WHERE (ARRAY['[?]', ?::text, $tag$[?]$tag$])[?] = ?`, []interface{}{7, "bound", 2, "bound"}, 7},
			{"json operators", `SELECT ?::integer AS id WHERE ?::jsonb ?| ARRAY[?::text, ?::text] AND ?::jsonb ?& ARRAY[?::text]`, []interface{}{9, `{"a":1}`, "missing", "a", `{"a":1}`, "a"}, 9},
		} {
			t.Run(test.name, func(t *testing.T) {
				rows, err := executor.QueryContext(ctx, test.sql, test.args...)
				assertRawBoundaryID(t, rows, err, test.want)
				if _, err := executor.QueryContext(ctx, test.sql, test.args[:len(test.args)-1]...); !errors.Is(err, db.ErrInvalidQuery) {
					t.Fatalf("missing array argument was not rejected before execution: %v", err)
				}
			})
		}
		if affected, err := executor.ExecuteContext(ctx, "UPDATE "+table+" SET numbers[?] = ? WHERE id = ?", 2, 23, 1); err != nil || affected != 1 {
			t.Fatalf("array subscript update: affected=%d err=%v", affected, err)
		}
		rows, err := executor.QueryContext(ctx, "SELECT id FROM "+table+" WHERE numbers[?] = ?", 2, 23)
		assertRawBoundaryID(t, rows, err, 1)
	}
	t.Run("database", func(t *testing.T) { exercise(t, database) })
	t.Run("transaction", func(t *testing.T) {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = transaction.Rollback() }()
		exercise(t, transaction)
	})
	t.Run("pinned", func(t *testing.T) {
		if err := database.WithPinnedSQLConnection(ctx, func(connection db.PinnedSQLConnection) error {
			exercise(t, connection)
			return connection.TransactionContext(ctx, func(transaction db.ContextualRawQueryable) error {
				t.Run("transaction", func(t *testing.T) { exercise(t, transaction) })
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
	})
	rows, err := database.Table(table).WithContext(ctx).Field("id").
		WhereRaw(`id = ANY(ARRAY[?::integer, ?::integer]) AND numbers[?] = ?`, 1, 2, 1, 11).
		Group("id").HavingRaw(`COUNT(*) = ANY(ARRAY[?::bigint, ?::bigint])`, 1, 2).Select()
	assertRawBoundaryID(t, rows, err, 1)
}
