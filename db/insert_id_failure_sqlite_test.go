//go:build cgo

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// Only result metadata is fault-injected; execution, stored rows and transaction
// finalization are supplied by the actual SQLite engine and database/sql.
type missingSQLiteIDConnector struct {
	cause       error
	rowsUnknown bool
}

func (c missingSQLiteIDConnector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }
func (c missingSQLiteIDConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	connection, err := c.Driver().Open(":memory:")
	if err != nil {
		return nil, err
	}
	return &missingSQLiteIDConn{Conn: connection, fault: c}, nil
}

type missingSQLiteIDConn struct {
	driver.Conn
	fault missingSQLiteIDConnector
}

func (c *missingSQLiteIDConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	exec, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	result, err := exec.ExecContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return missingSQLiteIDResult{Result: result, fault: c.fault}, nil
}
func (c *missingSQLiteIDConn) Prepare(query string) (driver.Stmt, error) {
	statement, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &missingSQLiteIDStmt{Stmt: statement, fault: c.fault}, nil
}

type missingSQLiteIDStmt struct {
	driver.Stmt
	fault missingSQLiteIDConnector
}

func (s *missingSQLiteIDStmt) Exec(args []driver.Value) (driver.Result, error) {
	result, err := s.Stmt.Exec(args)
	if err != nil {
		return nil, err
	}
	return missingSQLiteIDResult{Result: result, fault: s.fault}, nil
}

type missingSQLiteIDResult struct {
	driver.Result
	fault missingSQLiteIDConnector
}

func (r missingSQLiteIDResult) LastInsertId() (int64, error) {
	if r.fault.rowsUnknown {
		return r.Result.LastInsertId()
	}
	return 987, r.fault.cause
}

func (r missingSQLiteIDResult) RowsAffected() (int64, error) {
	if r.fault.rowsUnknown {
		return 987, r.fault.cause
	}
	return r.Result.RowsAffected()
}

func TestInsertIDFailureSQLitePersistenceAndRollback(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, action := range []string{"autocommit", "rollback", "commit", "callback_rollback"} {
			name := action + "/uncached"
			if cached {
				name = action + "/prepared"
			}
			t.Run(name, func(t *testing.T) {
				cause := errors.New("injected SQLite result ID failure")
				raw := sql.OpenDB(missingSQLiteIDConnector{cause: cause})
				raw.SetMaxOpenConns(1)
				connection := &SQLConnection{DB: raw, Builder: &builder.Sqlite{}}
				if cached {
					connection = NewSQLConnection(raw, &builder.Sqlite{})
				}
				database := NewDB(connection)
				t.Cleanup(func() {
					if err := database.Close(); err != nil {
						t.Error(err)
					}
				})
				if _, err := raw.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE)"); err != nil {
					t.Fatal(err)
				}
				var operationErr error
				expected := 1
				switch action {
				case "autocommit":
					_, operationErr = database.Name("users").InsertGetId(map[string]any{"name": "Ada"})
				case "callback_rollback":
					operationErr = database.Transaction(func(tx *Tx) error {
						_, err := tx.Name("users").InsertGetId(map[string]any{"name": "Ada"})
						return err
					})
					expected = 0
				default:
					tx, err := database.Begin()
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					_, operationErr = tx.Name("users").InsertGetId(map[string]any{"name": "Ada"})
					var inTransaction int
					if err := tx.tx.QueryRow("SELECT COUNT(*) FROM users").Scan(&inTransaction); err != nil || inTransaction != 1 {
						t.Fatalf("INSERT did not execute in transaction: count=%d error=%v", inTransaction, err)
					}
					if action == "rollback" {
						err = tx.Rollback()
						expected = 0
					} else {
						err = tx.Commit()
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				var actual int
				if err := raw.QueryRow("SELECT COUNT(*) FROM users").Scan(&actual); err != nil {
					t.Fatal(err)
				}
				if actual != expected {
					t.Fatalf("metadata error must not change persistence/finalization: got=%d want=%d", actual, expected)
				}
				t.Logf("SQLite row count after %s: %d", action, actual)
				partial := requireInsertIDPartial(t, operationErr, cause)
				if partial.Result.Data["name"] != "Ada" {
					t.Fatal("partial write snapshot missing")
				}
				if expected == 1 {
					var actualID int64
					if err := raw.QueryRow("SELECT id FROM users WHERE name = ?", "Ada").Scan(&actualID); err != nil || actualID != 1 {
						t.Fatalf("expected real generated ID, not injected 987: id=%d error=%v", actualID, err)
					}
				}
			})
		}
	}
}

func TestRowsAffectedFailureSQLiteTransactionOwner(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, action := range []string{"autocommit", "commit", "rollback"} {
			t.Run(action, func(t *testing.T) {
				cause := errors.New("驱动无法读取行数")
				raw := sql.OpenDB(missingSQLiteIDConnector{cause: cause, rowsUnknown: true})
				raw.SetMaxOpenConns(1)
				connection := &SQLConnection{DB: raw, Builder: &builder.Sqlite{}}
				if cached {
					connection = NewSQLConnection(raw, &builder.Sqlite{})
				}
				database := NewDB(connection)
				t.Cleanup(func() { _ = database.Close() })
				if _, err := raw.Exec("CREATE TABLE records (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
					t.Fatal(err)
				}
				type record struct {
					*Model
					ID   int64
					Name string
				}
				row := &record{Name: "Ada"}
				model, err := NewModelFor(t.Context(), database, row)
				if err != nil {
					t.Fatal(err)
				}
				model.Table("records")
				var tx *Tx
				if action != "autocommit" {
					tx, err = database.Begin()
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					model = model.WithTx(tx)
				}
				err = model.Save()
				if !errors.Is(err, ErrRowsAffectedUnknown) || !errors.Is(err, cause) || errors.Is(err, ErrPartialWrite) {
					t.Fatalf("首次写入的阶段或原始错误丢失：%v", err)
				}
				if err := row.Save(); !errors.Is(err, ErrRowsAffectedUnknown) || row.ID != 0 {
					t.Fatalf("另一句柄未阻止重放或生成了主键：%v %d", err, row.ID)
				}
				want := 1
				if tx != nil {
					var inside int
					if err := tx.tx.QueryRow("SELECT COUNT(*) FROM records").Scan(&inside); err != nil || inside != 1 {
						t.Fatalf("事务内实际执行次数错误：%d %v", inside, err)
					}
					if err := row.Refresh(); !errors.Is(err, ErrModelRecordTransaction) {
						t.Fatalf("丢失待定事务的归属：%v", err)
					}
					if action == "rollback" {
						err = tx.Rollback()
						want = 0
					} else {
						err = tx.Commit()
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				var count int
				if err := raw.QueryRow("SELECT COUNT(*) FROM records").Scan(&count); err != nil || count != want {
					t.Fatalf("持久记录数量错误：%d %v", count, err)
				}
			})
		}
	}
}
