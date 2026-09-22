package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"
)

type statementCacheDriverState struct {
	prepares       atomic.Int64
	closes         atomic.Int64
	active         atomic.Int64
	maxActive      atomic.Int64
	queryStarted   chan struct{}
	queryRelease   chan struct{}
	prepareQuery   string
	prepareStarted chan struct{}
	prepareRelease chan struct{}
}

type statementCacheDriver struct {
	state *statementCacheDriverState
}

func (d *statementCacheDriver) Open(string) (driver.Conn, error) {
	return &statementCacheDriverConnection{state: d.state}, nil
}

type statementCacheDriverConnection struct {
	state *statementCacheDriverState
}

func (c *statementCacheDriverConnection) Prepare(query string) (driver.Stmt, error) {
	c.state.prepares.Add(1)
	return &statementCacheDriverStatement{state: c.state, query: query}, nil
}

// PrepareContext 允许测试精确控制冷语句预编译，验证取消与其他语句的隔离。
func (c *statementCacheDriverConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if query == c.state.prepareQuery && c.state.prepareRelease != nil {
		c.state.prepareStarted <- struct{}{}
		select {
		case <-c.state.prepareRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.Prepare(query)
}

func (*statementCacheDriverConnection) Close() error { return nil }

func (*statementCacheDriverConnection) Begin() (driver.Tx, error) {
	return statementCacheDriverTransaction{}, nil
}

type statementCacheDriverTransaction struct{}

func (statementCacheDriverTransaction) Commit() error   { return nil }
func (statementCacheDriverTransaction) Rollback() error { return nil }

type statementCacheDriverStatement struct {
	state *statementCacheDriverState
	query string
}

func (s *statementCacheDriverStatement) Close() error {
	s.state.closes.Add(1)
	return nil
}

func (*statementCacheDriverStatement) NumInput() int { return -1 }

func (*statementCacheDriverStatement) Exec([]driver.Value) (driver.Result, error) {
	return statementCacheDriverResult{}, nil
}

func (s *statementCacheDriverStatement) Query([]driver.Value) (driver.Rows, error) {
	return &statementCacheDriverRows{query: s.query}, nil
}

func (*statementCacheDriverStatement) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	return statementCacheDriverResult{}, nil
}

func (s *statementCacheDriverStatement) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	if s.state.queryRelease != nil {
		active := s.state.active.Add(1)
		for {
			maximum := s.state.maxActive.Load()
			if active <= maximum || s.state.maxActive.CompareAndSwap(maximum, active) {
				break
			}
		}
		if s.state.queryStarted != nil {
			s.state.queryStarted <- struct{}{}
		}
		<-s.state.queryRelease
		s.state.active.Add(-1)
	}
	return &statementCacheDriverRows{query: s.query}, nil
}

type statementCacheDriverResult struct{}

func (statementCacheDriverResult) LastInsertId() (int64, error) { return 1, nil }
func (statementCacheDriverResult) RowsAffected() (int64, error) { return 1, nil }

type statementCacheDriverRows struct {
	query string
	read  bool
}

func (*statementCacheDriverRows) Columns() []string { return []string{"value"} }
func (*statementCacheDriverRows) Close() error      { return nil }

func (rows *statementCacheDriverRows) Next(destination []driver.Value) error {
	if rows.read {
		return io.EOF
	}
	rows.read = true
	destination[0] = int64(1)
	return nil
}

var statementCacheDriverSequence atomic.Uint64

func newStatementCacheConnection(t *testing.T) (*SQLConnection, *statementCacheDriverState) {
	t.Helper()
	state := &statementCacheDriverState{}
	driverName := fmt.Sprintf("thinkgo_statement_cache_%d", statementCacheDriverSequence.Add(1))
	sql.Register(driverName, &statementCacheDriver{state: state})
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("打开语句缓存测试驱动失败: %v", err)
	}
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = handle.Close() })
	return NewSQLConnection(handle, &builder.Sqlite{}), state
}

func newConcurrentStatementCacheConnection(t *testing.T) (*SQLConnection, *statementCacheDriverState) {
	t.Helper()
	state := &statementCacheDriverState{}
	driverName := fmt.Sprintf("thinkgo_statement_cache_concurrent_%d", statementCacheDriverSequence.Add(1))
	sql.Register(driverName, &statementCacheDriver{state: state})
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("打开并发语句缓存测试驱动失败: %v", err)
	}
	handle.SetMaxOpenConns(4)
	handle.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = handle.Close() })
	return NewSQLConnection(handle, &builder.Sqlite{}), state
}

// TestSQLConnectionReusesPreparedStatements 验证重复 SQL 会复用连接级预编译语句。
func TestSQLConnectionReusesPreparedStatements(t *testing.T) {
	connection, state := newStatementCacheConnection(t)
	for range 2 {
		rows, err := connection.Query("SELECT value FROM cached_rows WHERE id = ?", 1)
		if err != nil || len(rows) != 1 || rows[0]["value"] != int64(1) {
			t.Fatalf("重复查询失败: rows=%#v err=%v", rows, err)
		}
	}
	if prepares := state.prepares.Load(); prepares != 1 {
		t.Fatalf("重复 SQL 未复用预编译语句，Prepare 次数=%d", prepares)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("关闭带缓存连接失败: %v", err)
	}
	if closes := state.closes.Load(); closes != 1 {
		t.Fatalf("连接关闭时未关闭缓存语句，Close 次数=%d", closes)
	}
}

// TestSQLConnectionReusesPreparedStatementsForExecute 验证写操作同样复用连接级预编译语句并保持结果。
func TestSQLConnectionReusesPreparedStatementsForExecute(t *testing.T) {
	connection, state := newStatementCacheConnection(t)
	for range 2 {
		affected, err := connection.Execute("UPDATE cached_rows SET value = ? WHERE id = ?", 2, 1)
		if err != nil || affected != 1 {
			t.Fatalf("重复执行写操作失败: affected=%d err=%v", affected, err)
		}
	}
	if prepares := state.prepares.Load(); prepares != 1 {
		t.Fatalf("重复执行 SQL 未复用预编译语句，Prepare 次数=%d", prepares)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("关闭写操作缓存连接失败: %v", err)
	}
	if closes := state.closes.Load(); closes != 1 {
		t.Fatalf("关闭连接时未关闭缓存语句，Close 次数=%d", closes)
	}
}

// TestSQLConnectionEvictsPreparedStatements 验证缓存达到容量后按先进先出淘汰。
func TestSQLConnectionEvictsPreparedStatements(t *testing.T) {
	connection, state := newStatementCacheConnection(t)
	connection.statementCapacity = 1
	queries := []string{
		"SELECT value FROM cache_first",
		"SELECT value FROM cache_second",
		"SELECT value FROM cache_first",
	}
	for _, query := range queries {
		if _, err := connection.Query(query); err != nil {
			t.Fatalf("缓存淘汰测试查询失败: %v", err)
		}
	}
	if prepares := state.prepares.Load(); prepares != int64(len(queries)) {
		t.Fatalf("淘汰后的 SQL 应重新 Prepare，次数=%d", prepares)
	}
	if closes := state.closes.Load(); closes < 2 {
		t.Fatalf("被淘汰语句未及时关闭，Close 次数=%d", closes)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("关闭淘汰测试连接失败: %v", err)
	}
}

// TestSQLConnectionStatementCacheAllowsConcurrentHits 验证缓存命中不会用全局互斥锁串行化查询执行。
func TestSQLConnectionStatementCacheAllowsConcurrentHits(t *testing.T) {
	connection, state := newConcurrentStatementCacheConnection(t)
	if _, err := connection.Query("SELECT value FROM concurrent_rows"); err != nil {
		t.Fatalf("预热语句缓存失败: %v", err)
	}
	state.queryStarted = make(chan struct{}, 2)
	state.queryRelease = make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := connection.Query("SELECT value FROM concurrent_rows")
			results <- err
		}()
	}
	for range 2 {
		select {
		case <-state.queryStarted:
		case <-time.After(time.Second):
			t.Fatal("缓存命中查询未能并行进入驱动")
		}
	}
	close(state.queryRelease)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("并发缓存查询失败: %v", err)
		}
	}
	if state.maxActive.Load() < 2 {
		t.Fatalf("缓存命中仍被串行化，最大并发数=%d", state.maxActive.Load())
	}
}
