package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// The original pinned-session fixture deliberately rejects Prepare. These public
// DB entry points also exercise the default statement cache, so their recording
// driver must support prepared and direct execution with the same recorder.
type postgresArrayRecordingDriver struct {
	*pinnedSQLDriver
}

func (d *postgresArrayRecordingDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.pinnedSQLDriver.Open(name)
	if err != nil {
		return nil, err
	}
	return &postgresArrayRecordingConnection{connection.(*pinnedSQLDriverConnection)}, nil
}

type postgresArrayRecordingConnection struct {
	*pinnedSQLDriverConnection
}

func (c *postgresArrayRecordingConnection) Prepare(query string) (driver.Stmt, error) {
	return &postgresArrayRecordingStatement{connection: c.pinnedSQLDriverConnection, query: query}, nil
}

type postgresArrayRecordingStatement struct {
	connection *pinnedSQLDriverConnection
	query      string
}

func (*postgresArrayRecordingStatement) Close() error {
	return nil
}

func (*postgresArrayRecordingStatement) NumInput() int {
	return -1
}

func (s *postgresArrayRecordingStatement) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), postgresArrayNamedValues(args))
}

func (s *postgresArrayRecordingStatement) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), postgresArrayNamedValues(args))
}

func (s *postgresArrayRecordingStatement) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.connection.ExecContext(ctx, s.query, args)
}

func (s *postgresArrayRecordingStatement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.connection.QueryContext(ctx, s.query, args)
}

func postgresArrayNamedValues(values []driver.Value) []driver.NamedValue {
	args := make([]driver.NamedValue, len(values))
	for index, value := range values {
		args[index] = driver.NamedValue{Ordinal: index + 1, Value: value}
	}
	return args
}

func newPostgresArrayValidationDatabase(t *testing.T) (*DB, *pinnedSQLDriverState) {
	t.Helper()
	state := &pinnedSQLDriverState{}
	driverName := fmt.Sprintf("thinkgo_postgres_array_%d", pinnedSQLDriverSequence.Add(1))
	sql.Register(driverName, &postgresArrayRecordingDriver{&pinnedSQLDriver{state: state}})
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	handle.SetMaxOpenConns(2)
	handle.SetMaxIdleConns(2)
	database := NewDB(NewSQLConnection(handle, &builder.Pgsql{}))
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

func TestIssue145PostgresArraysReachEveryRawExecutor(t *testing.T) {
	database, state := newPostgresArrayValidationDatabase(t)
	ctx := context.Background()
	exercise := func(t *testing.T, executor ContextualRawQueryable) {
		t.Helper()
		for _, test := range []struct {
			name string
			sql  string
			want string
			args []interface{}
		}{
			{"constructor", `SELECT ARRAY[?, ?], ?`, `SELECT ARRAY[$1, $2], $3`, []interface{}{1, 2, 3}},
			{"subscript", `SELECT numbers[?] FROM records WHERE id = ?`, `SELECT numbers[$1] FROM records WHERE id = $2`, []interface{}{1, 2}},
			{"slice", `SELECT numbers[?:?], ? FROM records`, `SELECT numbers[$1:$2], $3 FROM records`, []interface{}{1, 2, 3}},
			{"nested", `SELECT ARRAY[ARRAY[?, ?], ARRAY[?, ?]], ?`, `SELECT ARRAY[ARRAY[$1, $2], ARRAY[$3, $4]], $5`, []interface{}{1, 2, 3, 4, 5}},
			{"quotes", `SELECT ARRAY['[?]', ?, 'it''s ?'], "odd[?]"[?], ?`, `SELECT ARRAY['[?]', $1, 'it''s ?'], "odd[?]"[$2], $3`, []interface{}{1, 2, 3}},
			{"comments", "SELECT ARRAY[? /* ] ? */, ? -- [?]\n], ?", "SELECT ARRAY[$1 /* ] ? */, $2 -- [?]\n], $3", []interface{}{1, 2, 3}},
			{"dollar quotes", `SELECT ARRAY[$tag$[?]$tag$, ?], ?`, `SELECT ARRAY[$tag$[?]$tag$, $1], $2`, []interface{}{1, 2}},
			{"operators", `SELECT data ?| ARRAY[?, ?], data ?& ARRAY[?], data ?? ?, data @? ?`, `SELECT data ?| ARRAY[$1, $2], data ?& ARRAY[$3], data ? $4, data @? $5`, []interface{}{"a", "b", "a", "a", "$.a"}},
		} {
			t.Run(test.name, func(t *testing.T) {
				if rows, err := executor.QueryContext(ctx, test.sql, test.args...); err != nil || len(rows) != 1 {
					t.Fatalf("public query rejected array bindings: rows=%v err=%v", rows, err)
				}
				_, _, operations := state.snapshot()
				if got := operations[len(operations)-1].query; got != test.want {
					t.Fatalf("driver SQL = %q; want %q", got, test.want)
				}
				before := len(operations)
				for _, invalid := range [][]interface{}{test.args[:len(test.args)-1], append(append([]interface{}{}, test.args...), "extra")} {
					if _, err := executor.QueryContext(ctx, test.sql, invalid...); !errors.Is(err, ErrInvalidQuery) {
						t.Fatalf("invalid argument count reached driver: %v", err)
					}
				}
				_, _, operations = state.snapshot()
				if len(operations) != before {
					t.Fatal("invalid query executed a driver operation")
				}
			})
		}
		const update = `UPDATE records SET numbers = ARRAY[?, ?] WHERE numbers[?] = ?`
		if affected, err := executor.ExecuteContext(ctx, update, 11, 22, 1, 11); err != nil || affected != 1 {
			t.Fatalf("public execute rejected array bindings: affected=%d err=%v", affected, err)
		}
		_, _, operations := state.snapshot()
		if got := operations[len(operations)-1].query; got != `UPDATE records SET numbers = ARRAY[$1, $2] WHERE numbers[$3] = $4` {
			t.Fatalf("execute rebound array parameters incorrectly: %q", got)
		}
		before := len(operations)
		if _, err := executor.ExecuteContext(ctx, update, 11, 22, 1); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("execute accepted a missing array binding: %v", err)
		}
		_, _, operations = state.snapshot()
		if len(operations) != before {
			t.Fatal("invalid execute reached the driver")
		}
	}
	t.Run("database", func(t *testing.T) { exercise(t, database) })
	t.Run("connection", func(t *testing.T) {
		if err := database.WithConnection(func(connection Connection) error {
			exercise(t, connection.(ContextualRawQueryable))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("transaction", func(t *testing.T) {
		transaction, err := database.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = transaction.Rollback() }()
		exercise(t, transaction)
	})
	t.Run("pinned", func(t *testing.T) {
		if err := database.WithPinnedSQLConnection(ctx, func(connection PinnedSQLConnection) error {
			exercise(t, connection)
			return connection.TransactionContext(ctx, func(transaction ContextualRawQueryable) error {
				t.Run("transaction", func(t *testing.T) { exercise(t, transaction) })
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestIssue145PostgresArraysInRawQueryClauses(t *testing.T) {
	database, state := newPostgresArrayValidationDatabase(t)
	query := database.Table("records").Field("id").
		WhereRaw(`id = ANY(ARRAY[?, ?]) AND numbers[?] = ?`, 1, 2, 1, 11).
		Group("id").HavingRaw(`COUNT(*) = ANY(ARRAY[?, ?])`, 1, 2)
	if rows, err := query.Select(); err != nil || len(rows) != 1 {
		t.Fatalf("WhereRaw/HavingRaw rejected valid arrays: rows=%v err=%v", rows, err)
	}
	_, _, operations := state.snapshot()
	got := operations[len(operations)-1].query
	if !strings.Contains(got, `WHERE id = ANY(ARRAY[$1, $2]) AND numbers[$3] = $4`) ||
		!strings.Contains(got, `HAVING COUNT(*) = ANY(ARRAY[$5, $6])`) {
		t.Fatalf("raw clauses lost parameter order: %q", got)
	}
	before := len(operations)
	for _, invalid := range []*Query{
		database.Table("records").WhereRaw(`id = ANY(ARRAY[?, ?])`, 1),
		database.Table("records").Group("id").HavingRaw(`COUNT(*) = ANY(ARRAY[?])`, 1, 2),
	} {
		if _, err := invalid.Select(); err == nil {
			t.Fatal("raw clause accepted an invalid array argument count")
		}
	}
	_, _, operations = state.snapshot()
	if len(operations) != before {
		t.Fatal("invalid raw clause reached the driver")
	}
}

func TestIssue145BracketIdentifierCompatibility(t *testing.T) {
	const query = `SELECT [column?], [escaped]]?] FROM [table] WHERE id = ?`
	for _, dialect := range []string{"", "mysql", "sqlite", "sqlserver", "oracle"} {
		if count, err := countSQLPlaceholders(query, dialect); err != nil || count != 1 {
			t.Fatalf("%q bracket identifier changed: count=%d err=%v", dialect, count, err)
		}
	}
	if count, err := countSQLPlaceholders(`SELECT ARRAY[?, ?], ?`, " Postgres "); err != nil || count != 3 {
		t.Fatalf("normalized PostgreSQL dialect lost arrays: count=%d err=%v", count, err)
	}
	connection, recorder := newRecordingHardeningSQLConnection(t)
	connection.Builder = &builder.Sqlsrv{}
	database := NewDB(connection)
	t.Cleanup(func() { _ = database.Close() })
	if rows, err := database.Query(query, 7); err != nil || len(rows) != 1 {
		t.Fatalf("SQL Server public query rejected bracket identifiers: rows=%v err=%v", rows, err)
	}
	if got := recorder.recordedQuery(); got != `SELECT [column?], [escaped]]?] FROM [table] WHERE id = @p1` {
		t.Fatalf("SQL Server bracket rebinding changed: %q", got)
	}
}
