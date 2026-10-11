package connector

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestSQLPoolBudgetEffectiveCapacity(t *testing.T) {
	for _, sample := range []struct {
		name    string
		config  db.Config
		want    int
		invalid bool
	}{
		{"defaults", db.Config{MaxOpenConnsLimit: 25}, 25, false},
		{"defaults_over_budget", db.Config{MaxOpenConnsLimit: 24}, 0, true},
		{"explicit_equal", db.Config{MaxOpenConns: 60, MaxOpenConnsLimit: 60}, 60, false},
		{"explicit_under", db.Config{MaxOpenConns: 60, MaxOpenConnsLimit: 80}, 60, false},
		{"explicit_over", db.Config{MaxOpenConns: 81, MaxOpenConnsLimit: 80}, 0, true},
		{"opt_out", db.Config{MaxOpenConns: 1000000}, 1000000, false},
		{"negative_budget", db.Config{MaxOpenConnsLimit: -1}, 0, true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			before := sample.config
			settings, err := resolveSQLConnectionPoolConfig(sample.config)
			if sample.invalid {
				if !errors.Is(err, db.ErrInvalidDatabaseConfig) || settings != (sqlConnectionPoolConfig{}) {
					t.Fatalf("settings=%+v err=%v", settings, err)
				}
			} else if err != nil || settings.MaxOpenConns != sample.want {
				t.Fatalf("settings=%+v err=%v", settings, err)
			}
			if sample.config.MaxOpenConns != before.MaxOpenConns || sample.config.MaxOpenConnsLimit != before.MaxOpenConnsLimit {
				t.Fatal("mutated requested capacity")
			}
		})
	}
	settings, err := sqlitePoolConfig(db.Config{Database: ":memory:", MaxOpenConnsLimit: 1})
	if err != nil || settings.MaxOpenConns != 1 || settings.MaxIdleConns != 1 || settings.ConnMaxLifetime != 0 {
		t.Fatalf("memory pool: %+v %v", settings, err)
	}
}

type poolBudgetProbe struct{ opens, pings atomic.Int32 }

func (probe *poolBudgetProbe) Open(string) (driver.Conn, error) {
	probe.opens.Add(1)
	return &poolBudgetConn{probe}, nil
}
func (probe *poolBudgetProbe) OpenConnector(string) (driver.Connector, error) {
	probe.opens.Add(1)
	return poolBudgetDriverConnector{probe}, nil
}

type poolBudgetDriverConnector struct{ probe *poolBudgetProbe }

func (c poolBudgetDriverConnector) Connect(context.Context) (driver.Conn, error) {
	return &poolBudgetConn{c.probe}, nil
}
func (c poolBudgetDriverConnector) Driver() driver.Driver { return c.probe }

type poolBudgetConn struct{ probe *poolBudgetProbe }

func (c *poolBudgetConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported prepare")
}
func (c *poolBudgetConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unsupported transaction")
}
func (c *poolBudgetConn) Close() error               { return nil }
func (c *poolBudgetConn) Ping(context.Context) error { c.probe.pings.Add(1); return nil }

var budgetDriverOnce sync.Once
var budgetDriverProbe poolBudgetProbe

func TestSQLPoolBudgetRejectsBeforeDriverAndPing(t *testing.T) {
	const name = "thinkgo_issue49_pool_budget_probe"
	budgetDriverOnce.Do(func() { sql.Register(name, &budgetDriverProbe) })
	opens, pings := budgetDriverProbe.opens.Load(), budgetDriverProbe.pings.Load()
	connection, err := openSQLConnection(name, "unused", &builder.Pgsql{}, db.Config{MaxOpenConnsLimit: 24})
	if connection != nil || !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("connection=%v err=%v", connection, err)
	}
	if budgetDriverProbe.opens.Load() != opens || budgetDriverProbe.pings.Load() != pings {
		t.Fatal("driver called before rejecting default capacity above budget")
	}
	handle := sql.OpenDB(poolBudgetDriverConnector{&budgetDriverProbe})
	connection, err = verifySQLConnection(handle, &builder.Pgsql{}, db.Config{MaxOpenConnsLimit: 24})
	if connection != nil || !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("verify error=%v", err)
	}
	if err := handle.PingContext(context.Background()); err == nil {
		t.Fatal("invalid pool handle was not closed")
	}
	if budgetDriverProbe.pings.Load() != pings {
		t.Fatal("invalid pool pinged backend")
	}
	handle = sql.OpenDB(poolBudgetDriverConnector{&budgetDriverProbe})
	connection, err = verifySQLConnection(handle, &builder.Pgsql{}, db.Config{MaxOpenConns: 12, MaxOpenConnsLimit: 20})
	if err != nil {
		_ = handle.Close()
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if handle.Stats().MaxOpenConnections != 12 {
		t.Fatalf("effective capacity=%d", handle.Stats().MaxOpenConnections)
	}
}

func TestSQLPoolBudgetRejectsBeforeSQLiteFileCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.db")
	connection, err := (&Sqlite{}).Connect(db.Config{Database: path, MaxOpenConnsLimit: 24})
	if connection != nil || !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("connection=%v err=%v", connection, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("over-budget config touched database file: %v", err)
	}
}
