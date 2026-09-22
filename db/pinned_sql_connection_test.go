package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"
)

type pinnedSQLDriverState struct {
	mu         sync.Mutex
	nextID     int64
	opened     []int64
	closed     []int64
	operations []pinnedSQLOperation
	blockedSQL string
	started    chan struct{}
	release    chan struct{}
	startOnce  sync.Once
}

type pinnedSQLOperation struct {
	connectionID int64
	query        string
}

func (state *pinnedSQLDriverState) recordOperation(connectionID int64, query string) {
	state.mu.Lock()
	state.operations = append(state.operations, pinnedSQLOperation{connectionID: connectionID, query: query})
	state.mu.Unlock()
}

func (state *pinnedSQLDriverState) snapshot() (opened, closed []int64, operations []pinnedSQLOperation) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]int64(nil), state.opened...), append([]int64(nil), state.closed...), append([]pinnedSQLOperation(nil), state.operations...)
}

type pinnedSQLDriver struct {
	state *pinnedSQLDriverState
}

func (current *pinnedSQLDriver) Open(string) (driver.Conn, error) {
	current.state.mu.Lock()
	current.state.nextID++
	id := current.state.nextID
	current.state.opened = append(current.state.opened, id)
	current.state.mu.Unlock()
	return &pinnedSQLDriverConnection{id: id, state: current.state}, nil
}

type pinnedSQLDriverConnection struct {
	id        int64
	state     *pinnedSQLDriverState
	closeOnce sync.Once
}

func (connection *pinnedSQLDriverConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("测试驱动不支持预编译")
}

func (connection *pinnedSQLDriverConnection) Close() error {
	connection.closeOnce.Do(func() {
		connection.state.mu.Lock()
		connection.state.closed = append(connection.state.closed, connection.id)
		connection.state.mu.Unlock()
	})
	return nil
}

func (connection *pinnedSQLDriverConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *pinnedSQLDriverConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	connection.state.recordOperation(connection.id, "BEGIN")
	return &pinnedSQLDriverTransaction{connection: connection}, nil
}

func (connection *pinnedSQLDriverConnection) Ping(context.Context) error { return nil }

func (connection *pinnedSQLDriverConnection) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	connection.state.recordOperation(connection.id, query)
	if query == connection.state.blockedSQL && connection.state.started != nil && connection.state.release != nil {
		connection.state.startOnce.Do(func() { close(connection.state.started) })
		<-connection.state.release
	}
	return driver.RowsAffected(1), nil
}

func (connection *pinnedSQLDriverConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	connection.state.recordOperation(connection.id, query)
	return &pinnedSQLDriverRows{connectionID: connection.id}, nil
}

type pinnedSQLDriverTransaction struct {
	connection *pinnedSQLDriverConnection
}

func (transaction *pinnedSQLDriverTransaction) Commit() error {
	transaction.connection.state.recordOperation(transaction.connection.id, "COMMIT")
	return nil
}

func (transaction *pinnedSQLDriverTransaction) Rollback() error {
	transaction.connection.state.recordOperation(transaction.connection.id, "ROLLBACK")
	return nil
}

type pinnedSQLDriverRows struct {
	connectionID int64
	read         bool
}

func (*pinnedSQLDriverRows) Columns() []string { return []string{"connection_id"} }
func (*pinnedSQLDriverRows) Close() error      { return nil }

func (rows *pinnedSQLDriverRows) Next(destination []driver.Value) error {
	if rows.read {
		return io.EOF
	}
	rows.read = true
	destination[0] = rows.connectionID
	return nil
}

var pinnedSQLDriverSequence atomic.Uint64

func newPinnedSQLTestDatabase(t *testing.T) (*DB, *pinnedSQLDriverState) {
	t.Helper()
	state := &pinnedSQLDriverState{}
	driverName := fmt.Sprintf("thinkgo_pinned_sql_%d", pinnedSQLDriverSequence.Add(1))
	sql.Register(driverName, &pinnedSQLDriver{state: state})
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("打开 pinned SQL 测试驱动失败: %v", err)
	}
	handle.SetMaxOpenConns(2)
	handle.SetMaxIdleConns(2)
	database := NewDB(NewSQLConnection(handle, &builder.Pgsql{}))
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

// TestWithPinnedSQLConnectionKeepsOnePhysicalSession 验证原生操作与内部事务始终落在同一物理连接。
func TestWithPinnedSQLConnectionKeepsOnePhysicalSession(t *testing.T) {
	database, state := newPinnedSQLTestDatabase(t)
	err := database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
		if connection.DialectName() != "postgres" {
			t.Fatalf("方言名称没有规范化并冻结: %q", connection.DialectName())
		}
		rows, queryErr := connection.QueryContext(context.Background(), "SELECT ?", 1)
		if queryErr != nil || len(rows) != 1 {
			return fmt.Errorf("pinned 查询失败: rows=%#v err=%w", rows, queryErr)
		}
		if _, executeErr := connection.ExecuteContext(context.Background(), "UPDATE records SET value = ?", 1); executeErr != nil {
			return executeErr
		}
		return connection.TransactionContext(context.Background(), func(transaction ContextualRawQueryable) error {
			if _, transactionErr := transaction.ExecuteContext(context.Background(), "DELETE FROM records WHERE value = ?", 1); transactionErr != nil {
				return transactionErr
			}
			_, transactionErr := transaction.QueryContext(context.Background(), "SELECT ?", 2)
			return transactionErr
		})
	})
	if err != nil {
		t.Fatalf("执行 pinned SQL 会话失败: %v", err)
	}
	_, _, operations := state.snapshot()
	if len(operations) < 6 {
		t.Fatalf("没有记录完整 pinned 操作: %#v", operations)
	}
	wantID := operations[0].connectionID
	for _, operation := range operations {
		if operation.connectionID != wantID {
			t.Fatalf("pinned 会话跨越了物理连接: %#v", operations)
		}
	}
}

// TestPinnedSQLConnectionInvalidateDiscardsPhysicalConnection 验证失效会话不能继续使用且不会回到连接池。
func TestPinnedSQLConnectionInvalidateDiscardsPhysicalConnection(t *testing.T) {
	database, state := newPinnedSQLTestDatabase(t)
	var firstID int64
	if err := database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
		rows, queryErr := connection.QueryContext(context.Background(), "SELECT 1")
		if queryErr != nil {
			return queryErr
		}
		firstID = rows[0]["connection_id"].(int64)
		connection.Invalidate()
		if _, executeErr := connection.ExecuteContext(context.Background(), "UPDATE records SET value = 1"); !errors.Is(executeErr, ErrPinnedConnectionInvalidated) {
			t.Fatalf("失效后仍可执行 SQL: %v", executeErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("回收失效 pinned 会话失败: %v", err)
	}
	var secondID int64
	if err := database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
		rows, queryErr := connection.QueryContext(context.Background(), "SELECT 1")
		if queryErr == nil {
			secondID = rows[0]["connection_id"].(int64)
		}
		return queryErr
	}); err != nil {
		t.Fatalf("创建后继 pinned 会话失败: %v", err)
	}
	if firstID == secondID {
		t.Fatalf("已失效物理连接被连接池复用: id=%d", firstID)
	}
	_, closed, _ := state.snapshot()
	if len(closed) == 0 || closed[0] != firstID {
		t.Fatalf("已失效物理连接没有关闭: first=%d closed=%v", firstID, closed)
	}
}

// TestWithPinnedSQLConnectionPanicDiscardsAndRethrows 验证 panic 路径先丢弃会话再保持原 panic。
func TestWithPinnedSQLConnectionPanicDiscardsAndRethrows(t *testing.T) {
	database, state := newPinnedSQLTestDatabase(t)
	panicValue := "pinned callback panic"
	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("没有保持原 panic: %#v", recovered)
			}
		}()
		_ = database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
			_, _ = connection.QueryContext(context.Background(), "SELECT 1")
			panic(panicValue)
		})
	}()
	opened, closed, _ := state.snapshot()
	if len(opened) != 1 || len(closed) != 1 || opened[0] != closed[0] {
		t.Fatalf("panic 后没有丢弃物理连接: opened=%v closed=%v", opened, closed)
	}
}

// TestWithPinnedSQLConnectionRejectsInvalidArguments 验证空依赖与已取消上下文不会进入 callback。
func TestWithPinnedSQLConnectionRejectsInvalidArguments(t *testing.T) {
	if err := (*DB)(nil).WithPinnedSQLConnection(context.Background(), func(PinnedSQLConnection) error { return nil }); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("空数据库错误不稳定: %v", err)
	}
	database, _ := newPinnedSQLTestDatabase(t)
	if err := database.WithPinnedSQLConnection(context.Background(), nil); !errors.Is(err, ErrInvalidDatabaseContext) {
		t.Fatalf("空 callback 错误不稳定: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := database.WithPinnedSQLConnection(ctx, func(PinnedSQLConnection) error {
		called = true
		return nil
	}); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("已取消上下文处理错误: called=%t err=%v", called, err)
	}
}

// TestPinnedSQLConnectionInvalidateStopsActiveTransaction 验证失效标记同样禁止已创建事务继续执行或提交。
func TestPinnedSQLConnectionInvalidateStopsActiveTransaction(t *testing.T) {
	database, _ := newPinnedSQLTestDatabase(t)
	err := database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
		return connection.TransactionContext(context.Background(), func(transaction ContextualRawQueryable) error {
			connection.Invalidate()
			if _, executeErr := transaction.ExecuteContext(context.Background(), "UPDATE records SET value = 2"); !errors.Is(executeErr, ErrPinnedConnectionInvalidated) {
				t.Fatalf("失效后事务仍可执行 SQL: %v", executeErr)
			}
			return nil
		})
	})
	if !errors.Is(err, ErrPinnedConnectionInvalidated) {
		t.Fatalf("失效事务仍然提交成功: %v", err)
	}
}

// TestPinnedSQLConnectionFinishObservesConcurrentInvalidation 验证 finish 等待在途 SQL 时不会丢失并发失效标记。
func TestPinnedSQLConnectionFinishObservesConcurrentInvalidation(t *testing.T) {
	database, state := newPinnedSQLTestDatabase(t)
	state.blockedSQL = "UPDATE blocked_records SET value = 1"
	state.started = make(chan struct{})
	state.release = make(chan struct{})
	callbackReturned := make(chan struct{})
	operationDone := make(chan error, 1)
	resultDone := make(chan error, 1)
	var pinned *pinnedSQLConnection
	go func() {
		resultDone <- database.WithPinnedSQLConnection(context.Background(), func(connection PinnedSQLConnection) error {
			pinned = connection.(*pinnedSQLConnection)
			go func() {
				_, operationErr := connection.ExecuteContext(context.Background(), state.blockedSQL)
				operationDone <- operationErr
			}()
			<-state.started
			close(callbackReturned)
			return nil
		})
	}()
	<-callbackReturned
	deadline := time.Now().Add(2 * time.Second)
	for {
		pinned.stateMu.Lock()
		closing := pinned.closing
		pinned.stateMu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pinned finish 没有进入等待状态")
		}
		time.Sleep(time.Millisecond)
	}
	pinned.Invalidate()
	close(state.release)
	if operationErr := <-operationDone; operationErr != nil {
		t.Fatalf("在途 SQL 非预期失败: %v", operationErr)
	}
	if resultErr := <-resultDone; resultErr != nil {
		t.Fatalf("并发失效清理失败: %v", resultErr)
	}
	opened, closed, _ := state.snapshot()
	if len(opened) != 1 || len(closed) != 1 || opened[0] != closed[0] {
		t.Fatalf("并发失效连接被归还连接池: opened=%v closed=%v", opened, closed)
	}
}

// TestTransactionRawQueryAndExecuteStayOnTransaction 验证公开 Tx 原生查询/执行路径
// 使用同一事务连接，并在事务终结后 fail-closed，避免误退回普通连接。
func TestTransactionRawQueryAndExecuteStayOnTransaction(t *testing.T) {
	database, state := newPinnedSQLTestDatabase(t)
	var nilTransaction *Tx
	if nilTransaction.Done() != nil {
		t.Fatal("空事务的 Done 必须返回 nil")
	}
	var nilContext context.Context
	if _, err := nilTransaction.QueryContext(nilContext, "SELECT 1"); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("空事务查询上下文错误不稳定: %v", err)
	}
	if _, err := nilTransaction.ExecuteContext(nilContext, "UPDATE records SET value = 1"); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("空事务执行上下文错误不稳定: %v", err)
	}
	if database.DialectName() != "postgres" {
		t.Fatalf("数据库方言名称不稳定: %q", database.DialectName())
	}
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("开启测试事务失败: %v", err)
	}
	rows, err := transaction.QueryContext(context.Background(), "SELECT value FROM records WHERE id = ?", 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("事务原生查询失败: rows=%#v err=%v", rows, err)
	}
	affected, err := transaction.ExecuteContext(context.Background(), "UPDATE records SET value = ?", 2)
	if err != nil || affected != 1 {
		t.Fatalf("事务原生执行失败: affected=%d err=%v", affected, err)
	}
	if transaction.Done() == nil {
		t.Fatal("有效事务必须提供 Done 通道")
	}
	if _, err = transaction.QueryContext(nilContext, "SELECT 1"); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("事务查询的空上下文错误不稳定: %v", err)
	}
	if _, err = transaction.ExecuteContext(nilContext, "UPDATE records SET value = 1"); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("事务执行的空上下文错误不稳定: %v", err)
	}
	if _, err = transaction.QueryContext(context.Background(), ""); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("事务空查询语句必须被拒绝: %v", err)
	}
	if _, err = transaction.ExecuteContext(context.Background(), ""); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("事务空执行语句必须被拒绝: %v", err)
	}
	if err = transaction.Rollback(); err != nil {
		t.Fatalf("回滚测试事务失败: %v", err)
	}
	if _, err = transaction.QueryContext(context.Background(), "SELECT 1"); !errors.Is(err, ErrTransactionDone) {
		t.Fatalf("事务终结后原生查询必须拒绝: %v", err)
	}
	_, _, operations := state.snapshot()
	if len(operations) < 4 {
		t.Fatalf("事务原生路径没有记录完整操作: %#v", operations)
	}
	connectionID := operations[0].connectionID
	for _, operation := range operations {
		if operation.connectionID != connectionID {
			t.Fatalf("事务原生操作跨越物理连接: %#v", operations)
		}
	}
}
