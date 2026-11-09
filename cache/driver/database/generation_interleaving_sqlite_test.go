//go:build cgo

package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type generationReadBarrier struct {
	armed    atomic.Bool
	consumed atomic.Bool
	entered  chan struct{}
	release  chan struct{}
	failure  string
	err      error
}

type generationSQLiteConnector struct {
	dsn     string
	barrier *generationReadBarrier
}

func (connector generationSQLiteConnector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }
func (connector generationSQLiteConnector) Connect(context.Context) (driver.Conn, error) {
	connection, err := connector.Driver().Open(connector.dsn)
	if err != nil {
		return nil, err
	}
	return &generationSQLiteConnection{SQLiteConn: connection.(*sqlite3.SQLiteConn), barrier: connector.barrier}, nil
}

type generationSQLiteConnection struct {
	*sqlite3.SQLiteConn
	barrier *generationReadBarrier
}

func (connection *generationSQLiteConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return connection.queryWithBarrier(args, func() (driver.Rows, error) {
		return connection.SQLiteConn.QueryContext(ctx, query, args)
	})
}

func (connection *generationSQLiteConnection) queryWithBarrier(args []driver.NamedValue, query func() (driver.Rows, error)) (driver.Rows, error) {
	generationQuery := false
	for _, argument := range args {
		generationQuery = generationQuery || argument.Value == dbGenerationKey
	}
	if generationQuery && connection.barrier.consumed.Load() && connection.barrier.failure == "read" {
		return nil, connection.barrier.err
	}
	rows, err := query()
	if err != nil || !generationQuery {
		return rows, err
	}
	return &generationBarrierRows{Rows: rows, barrier: connection.barrier}, nil
}

func (connection *generationSQLiteConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	statement, err := connection.SQLiteConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &generationSQLiteStatement{SQLiteStmt: statement.(*sqlite3.SQLiteStmt), connection: connection, query: query}, nil
}

type generationSQLiteStatement struct {
	*sqlite3.SQLiteStmt
	connection *generationSQLiteConnection
	query      string
}

func (statement *generationSQLiteStatement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return statement.connection.queryWithBarrier(args, func() (driver.Rows, error) {
		return statement.SQLiteStmt.QueryContext(ctx, args)
	})
}

func (statement *generationSQLiteStatement) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	barrier := statement.connection.barrier
	if barrier.consumed.Load() && barrier.failure == "delete" && strings.HasPrefix(strings.ToUpper(statement.query), "DELETE ") {
		return nil, barrier.err
	}
	return statement.SQLiteStmt.ExecContext(ctx, args)
}

func (connection *generationSQLiteConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if connection.barrier.consumed.Load() && connection.barrier.failure == "delete" && strings.HasPrefix(strings.ToUpper(query), "DELETE ") {
		return nil, connection.barrier.err
	}
	return connection.SQLiteConn.ExecContext(ctx, query, args)
}

type generationBarrierRows struct {
	driver.Rows
	barrier *generationReadBarrier
}

func (rows *generationBarrierRows) Next(values []driver.Value) error {
	err := rows.Rows.Next(values)
	if err == nil && rows.barrier.armed.CompareAndSwap(true, false) {
		// 旧代已经由 SQLite 实际读出，但尚未交还缓存写入方；WAL 允许另一连接池清理。
		rows.barrier.consumed.Store(true)
		close(rows.barrier.entered)
		<-rows.barrier.release
	}
	return err
}

func newGenerationInterleavingDrivers(t *testing.T, failure string) (*DB, *DB, *generationReadBarrier) {
	t.Helper()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "generation.db"))
	if filepath.VolumeName(path) != "" {
		path = "/" + path
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_busy_timeout=10000&_journal_mode=WAL"
	barrier := &generationReadBarrier{entered: make(chan struct{}), release: make(chan struct{}), failure: failure, err: errors.New("注入代际回收失败")}
	firstPool := sql.OpenDB(generationSQLiteConnector{dsn: dsn, barrier: barrier})
	secondPool, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	backends := make([]*DB, 0, 2)
	for _, pool := range []*sql.DB{firstPool, secondPool} {
		pool.SetMaxOpenConns(4)
		connection := db.NewSQLConnection(pool, &builder.Sqlite{})
		t.Cleanup(func() { _ = connection.Close() })
		backend, err := NewDB(connection, "generation_cache")
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.EnsureTable(); err != nil {
			t.Fatal(err)
		}
		backends = append(backends, backend)
	}
	if _, err := backends[1].currentGeneration(); err != nil {
		t.Fatal(err)
	}
	return backends[0], backends[1], barrier
}

// TestSQLiteLateGenerationWritesAreReclaimed 固定 Clear 成功后旧代再提交的交错，覆盖所有写入入口。
func TestSQLiteLateGenerationWritesAreReclaimed(t *testing.T) {
	for _, operation := range []string{"set", "increment", "decrement", "lease"} {
		for _, failure := range []string{"", "read", "delete"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				writer, clearer, barrier := newGenerationInterleavingDrivers(t, failure)
				const key, lockKey, owner = "value", "active-lease", "owner"
				if acquired, err := clearer.AcquireLock(lockKey, owner, time.Minute); err != nil || !acquired {
					t.Fatal("无法取得基线租约", err)
				}
				barrier.armed.Store(true)
				finished := make(chan error, 1)
				var once sync.Once
				release := func() { once.Do(func() { close(barrier.release) }) }
				defer release()
				go func() {
					var err error
					switch operation {
					case "set":
						err = writer.Set(key, "old-generation", 0)
					case "increment":
						_, err = writer.Inc(key, 1)
					case "decrement":
						_, err = writer.Dec(key, 1)
					case "lease":
						_, err = writer.UpdateIfLockOwnerContext(context.Background(), key, 0, lockKey, owner, func(interface{}, bool) (interface{}, bool, error) { return "old-generation", false, nil })
					}
					finished <- err
				}()
				const waitBudget = 5 * time.Second
				select {
				case <-barrier.entered:
				case <-time.After(waitBudget):
					t.Fatal("写入未到达旧代读取屏障")
				}
				if err := clearer.Clear(); err != nil {
					t.Fatal(err)
				}
				if err := clearer.Set(key, "current-generation", 0); err != nil {
					t.Fatal(err)
				}
				release()
				select {
				case err := <-finished:
					if failure == "" && err != nil || failure != "" && !errors.Is(err, barrier.err) {
						t.Fatalf("写入或回收结果不符: %v", err)
					}
				case <-time.After(waitBudget):
					t.Fatal("迟到写入未有界完成")
				}
				if value, found, err := clearer.Get(key); err != nil || !found || value != "current-generation" {
					t.Fatalf("旧代写入影响当前代: %v, %t, %v", value, found, err)
				}
				if renewed, err := clearer.RenewLock(lockKey, owner, time.Minute); err != nil || !renewed {
					t.Fatalf("清理破坏活动租约: %t, %v", renewed, err)
				}
				rows, err := db.NewDB(clearer.conn).Table(clearer.table).WhereLike("key", dbDataStoragePrefix+"%").Where("key", "<", generationPrefix(dbInitialGeneration+1)).Select()
				if err != nil || failure == "" && len(rows) != 0 || failure != "" && len(rows) != 1 {
					t.Fatalf("迟到旧代行回收不符: 行数=%d, 错误=%v", len(rows), err)
				}
			})
		}
	}
}
