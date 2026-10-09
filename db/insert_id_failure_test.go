package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// The driver distinguishes a completed Exec from a later metadata failure.
// Real SQLite row/rollback verification is in the separate integration test.
type insertIDFailureState struct {
	idErr, execErr                 error
	execs, ids, commits, rollbacks atomic.Int64
}
type insertIDFailureConnector struct{ state *insertIDFailureState }

func (c insertIDFailureConnector) Connect(context.Context) (driver.Conn, error) {
	return &insertIDFailureConn{c.state}, nil
}
func (c insertIDFailureConnector) Driver() driver.Driver { return insertIDFailureDriver{c.state} }

type insertIDFailureDriver struct{ state *insertIDFailureState }

func (d insertIDFailureDriver) Open(string) (driver.Conn, error) {
	return &insertIDFailureConn{d.state}, nil
}

type insertIDFailureConn struct{ state *insertIDFailureState }

func (*insertIDFailureConn) Close() error { return nil }
func (c *insertIDFailureConn) Prepare(string) (driver.Stmt, error) {
	return &insertIDFailureStmt{c}, nil
}
func (c *insertIDFailureConn) Begin() (driver.Tx, error) { return &insertIDFailureTx{c.state}, nil }
func (c *insertIDFailureConn) ExecContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.state.execs.Add(1)
	if c.state.execErr != nil {
		return nil, c.state.execErr
	}
	return insertIDFailureResult{c.state}, nil
}

type insertIDFailureStmt struct{ connection *insertIDFailureConn }

func (*insertIDFailureStmt) Close() error  { return nil }
func (*insertIDFailureStmt) NumInput() int { return -1 }
func (s *insertIDFailureStmt) Exec([]driver.Value) (driver.Result, error) {
	return s.connection.ExecContext(context.Background(), "", nil)
}
func (*insertIDFailureStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("unexpected query in INSERT result test")
}

type insertIDFailureResult struct{ state *insertIDFailureState }

func (r insertIDFailureResult) LastInsertId() (int64, error) {
	r.state.ids.Add(1)
	// Even nonzero metadata is untrusted when returned together with an error.
	return 73, r.state.idErr
}
func (insertIDFailureResult) RowsAffected() (int64, error) { return 1, nil }

type insertIDFailureTx struct{ state *insertIDFailureState }

func (tx *insertIDFailureTx) Commit() error {
	tx.state.commits.Add(1)
	return nil
}
func (tx *insertIDFailureTx) Rollback() error {
	tx.state.rollbacks.Add(1)
	return nil
}

func newInsertIDFailureDB(t *testing.T, state *insertIDFailureState, cached bool) (*DB, *SQLConnection) {
	t.Helper()
	raw := sql.OpenDB(insertIDFailureConnector{state})
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
	return database, connection
}
func requireInsertIDPartial(t *testing.T, err, cause error) *PartialWriteError {
	t.Helper()
	var partial *PartialWriteError
	if !errors.Is(err, ErrPartialWrite) || !errors.Is(err, cause) || !errors.As(err, &partial) {
		t.Fatalf("executed INSERT must retain partial result and driver cause: %v", err)
	}
	if partial.Result.Affected != 1 || partial.Result.IDKnown || partial.Result.ID != nil {
		t.Fatalf("invalid partial result: %#v", partial.Result)
	}
	if _, idErr := partial.Result.InsertedID(); !errors.Is(idErr, ErrInsertIDUnavailable) {
		t.Fatalf("unknown ID was invented: %v", idErr)
	}
	return partial
}

func TestInsertIDFailurePreservesResultAcrossEntryPoints(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, entry := range []string{"sql", "query", "model_map", "model_create"} {
			name := entry + "/uncached"
			if cached {
				name = entry + "/prepared"
			}
			t.Run(name, func(t *testing.T) {
				cause := errors.New("driver cannot report insert ID")
				state := &insertIDFailureState{idErr: cause}
				database, connection := newInsertIDFailureDB(t, state, cached)
				data := map[string]any{"name": "Ada", "payload": []byte{1, 2}}
				var err error
				switch entry {
				case "sql":
					var result InsertResult
					result, err = connection.Insert(context.Background(), newInsertRequest("users", data, "id", true))
					if errors.Is(err, ErrPartialWrite) && (result.Affected != 1 || result.ID != nil || result.IDKnown) {
						t.Fatalf("direct result lost: %#v", result)
					}
				case "query":
					var id any
					id, err = database.Name("users").InsertGetId(data)
					if id != nil {
						t.Fatalf("unknown ID must stay nil: %v", id)
					}
				case "model_map":
					_, err = NewModel(database, "users").InsertGetId(data)
				case "model_create":
					row := &struct {
						ID      int64
						Name    string
						Payload []byte
					}{Name: "Ada", Payload: []byte{1, 2}}
					err = NewModel(database, "users").Create(row)
					if row.ID != 0 {
						t.Fatalf("unknown ID assigned: %d", row.ID)
					}
				}
				partial := requireInsertIDPartial(t, err, cause)
				if partial.Result.Data["name"] != "Ada" || !reflect.DeepEqual(partial.Result.Data["payload"], []byte{1, 2}) {
					t.Fatalf("write snapshot lost: %#v", partial.Result.Data)
				}
				data["name"] = "changed"
				data["payload"].([]byte)[0] = 9
				if partial.Result.Data["name"] != "Ada" || partial.Result.Data["payload"].([]byte)[0] != 1 {
					t.Fatal("partial result aliases caller data")
				}
				if state.execs.Load() != 1 || state.ids.Load() != 1 {
					t.Fatalf("unexpected replay or ID lookup: %d/%d", state.execs.Load(), state.ids.Load())
				}
				if state.commits.Load() != 0 || state.rollbacks.Load() != 0 {
					t.Fatal("nontransactional INSERT finalized a transaction")
				}
			})
		}
	}
}

func TestInsertIDFailurePreservesTransactionControl(t *testing.T) {
	for _, action := range []string{"rollback", "commit", "callback_rollback"} {
		t.Run(action, func(t *testing.T) {
			cause := errors.New("ID unavailable after transaction write")
			state := &insertIDFailureState{idErr: cause}
			database, _ := newInsertIDFailureDB(t, state, false)
			var operationErr error
			if action == "callback_rollback" {
				operationErr = database.Transaction(func(tx *Tx) error {
					_, err := tx.Name("users").InsertGetId(map[string]any{"name": "Ada"})
					return err
				})
			} else {
				tx, err := database.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				_, operationErr = tx.Name("users").InsertGetId(map[string]any{"name": "Ada"})
				if state.commits.Load() != 0 || state.rollbacks.Load() != 0 {
					t.Fatal("metadata failure finalized caller transaction")
				}
				if action == "rollback" {
					err = tx.Rollback()
				} else {
					err = tx.Commit()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			requireInsertIDPartial(t, operationErr, cause)
			if state.execs.Load() != 1 || state.ids.Load() != 1 {
				t.Fatal("transaction INSERT replayed")
			}
			if action == "commit" {
				if state.commits.Load() != 1 || state.rollbacks.Load() != 0 {
					t.Fatal("explicit commit not preserved")
				}
			} else if state.commits.Load() != 0 || state.rollbacks.Load() != 1 {
				t.Fatal("rollback not preserved")
			}
		})
	}
}

func TestInsertIDFailureBlocksBoundRecordReplay(t *testing.T) {
	type boundRow struct {
		*Model
		ID   int64
		Name string
	}
	cause := errors.New("ID metadata failed")
	state := &insertIDFailureState{idErr: cause}
	database, _ := newInsertIDFailureDB(t, state, false)
	row := &boundRow{Name: "Ada"}
	if _, err := NewModelFor(context.Background(), database, row); err != nil {
		t.Fatal(err)
	}
	initialErr := row.Save()
	if err := row.Save(); !errors.Is(err, ErrPartialWrite) {
		t.Errorf("uncertain record allowed another save: %v", err)
	}
	if err := row.Create(row); !errors.Is(err, ErrPartialWrite) {
		t.Errorf("uncertain record allowed another create: %v", err)
	}
	if state.execs.Load() != 1 || row.ID != 0 {
		t.Fatalf("replayed INSERT or fake ID: calls=%d id=%d", state.execs.Load(), row.ID)
	}
	requireInsertIDPartial(t, initialErr, cause)
}

func TestInsertIDFailureDoesNotChangeOtherOutcomes(t *testing.T) {
	t.Run("plain_insert_does_not_read_id", func(t *testing.T) {
		state := &insertIDFailureState{idErr: errors.New("unused ID failure")}
		database, _ := newInsertIDFailureDB(t, state, true)
		affected, err := database.Name("users").Insert(map[string]any{"name": "Ada"})
		if err != nil || affected != 1 || state.ids.Load() != 0 {
			t.Fatalf("plain Insert changed: %d %v", affected, err)
		}
	})
	t.Run("successful_id", func(t *testing.T) {
		state := &insertIDFailureState{}
		database, _ := newInsertIDFailureDB(t, state, true)
		id, err := database.Name("users").InsertGetId(map[string]any{"name": "Ada"})
		if err != nil || id != int64(73) || state.execs.Load() != 1 {
			t.Fatalf("successful InsertGetId changed: %v %v", id, err)
		}
	})
	t.Run("execute_failure", func(t *testing.T) {
		cause := errors.New("execution rejected")
		state := &insertIDFailureState{execErr: cause}
		database, _ := newInsertIDFailureDB(t, state, false)
		_, err := database.Name("users").InsertGetId(map[string]any{"name": "Ada"})
		if err != cause || errors.Is(err, ErrPartialWrite) || state.ids.Load() != 0 {
			t.Fatalf("execute error misclassified: %v", err)
		}
	})
	t.Run("cancelled_before_execute", func(t *testing.T) {
		state := &insertIDFailureState{}
		database, _ := newInsertIDFailureDB(t, state, false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := database.Name("users").WithContext(ctx).InsertGetId(map[string]any{"name": "Ada"})
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrPartialWrite) || state.execs.Load() != 0 {
			t.Fatalf("prewrite cancellation misclassified: %v", err)
		}
	})
}
