package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"thinkgo/framework/db/builder"
)

type rollbackFailureDriver struct{ rollbackErr error }

func (d *rollbackFailureDriver) Open(string) (driver.Conn, error) {
	return &rollbackFailureConnection{rollbackErr: d.rollbackErr}, nil
}

type finalizationCountingDriver struct {
	commitCalls   atomic.Int64
	rollbackCalls atomic.Int64
}

var finalizationDriverSequence atomic.Uint64

func (driverState *finalizationCountingDriver) Open(string) (driver.Conn, error) {
	return &finalizationCountingConnection{driverState: driverState}, nil
}

type finalizationCountingConnection struct {
	driverState *finalizationCountingDriver
}

func (connection *finalizationCountingConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("test driver does not support prepared statements")
}
func (connection *finalizationCountingConnection) Close() error { return nil }
func (connection *finalizationCountingConnection) Begin() (driver.Tx, error) {
	return &finalizationCountingTransaction{driverState: connection.driverState}, nil
}

type finalizationCountingTransaction struct {
	driverState *finalizationCountingDriver
}

func (transaction *finalizationCountingTransaction) Commit() error {
	transaction.driverState.commitCalls.Add(1)
	return nil
}
func (transaction *finalizationCountingTransaction) Rollback() error {
	transaction.driverState.rollbackCalls.Add(1)
	return nil
}

func newFinalizationCountingDatabase(t *testing.T) (*DB, *finalizationCountingDriver) {
	t.Helper()
	driverState := &finalizationCountingDriver{}
	driverName := fmt.Sprintf("thinkgo_finalize_%d", finalizationDriverSequence.Add(1))
	sql.Register(driverName, driverState)
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open finalization test driver: %v", err)
	}
	return NewDB(&SQLConnection{DB: handle, Builder: &builder.Sqlite{}}), driverState
}

func waitForFinalizationCount(t *testing.T, driverState *finalizationCountingDriver, before int64) int64 {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		after := driverState.commitCalls.Load() + driverState.rollbackCalls.Load()
		if after != before {
			return after - before
		}
		time.Sleep(time.Millisecond)
	}
	return driverState.commitCalls.Load() + driverState.rollbackCalls.Load() - before
}

func TestBeginTxContextCancellationReleasesLease(t *testing.T) {
	database, driverState := newFinalizationCountingDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	cancel()
	select {
	case <-transaction.Done():
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not finalize transaction wrapper")
	}

	closed := make(chan error, 1)
	go func() { closed <- database.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close database: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("database close still waits for canceled transaction lease")
	}
	if finalized := waitForFinalizationCount(t, driverState, 0); finalized != 1 || driverState.rollbackCalls.Load() != 1 {
		t.Fatalf("finalized=%d rollback calls=%d, want one rollback", finalized, driverState.rollbackCalls.Load())
	}
}

func TestCommitAndCancellationFinalizeOnce(t *testing.T) {
	database, driverState := newFinalizationCountingDatabase(t)
	defer func() { _ = database.Close() }()
	for iteration := 0; iteration < 100; iteration++ {
		before := driverState.commitCalls.Load() + driverState.rollbackCalls.Load()
		ctx, cancel := context.WithCancel(context.Background())
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("iteration %d begin: %v", iteration, err)
		}
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); _ = transaction.Commit() }()
		go func() { defer wait.Done(); cancel() }()
		wait.Wait()
		select {
		case <-transaction.Done():
		case <-time.After(time.Second):
			t.Fatalf("iteration %d did not finalize", iteration)
		}
		if finalized := waitForFinalizationCount(t, driverState, before); finalized != 1 {
			t.Fatalf("iteration %d finalized %d times", iteration, finalized)
		}
	}
}

// TestTransactionExecutesCompleteQuerySurface 验证事务内查询、插入、更新、表达式更新、
// 删除、计数及高级查询始终使用同一事务执行器。
func TestTransactionExecutesCompleteQuerySurface(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	transaction, err := database.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	if rows, err := transaction.Table("users").WhereField("id", "=", 1).Select(); err != nil || len(rows) != 1 {
		t.Fatalf("事务查询失败: rows=%#v err=%v", rows, err)
	}
	idValue, err := transaction.Table("users").InsertGetId(map[string]interface{}{"name": "Ada"})
	if id, ok := idValue.(int64); err != nil || !ok || id != 7 {
		t.Fatalf("事务插入失败: id=%d err=%v", id, err)
	}
	if affected, err := transaction.Table("users").WhereField("id", "=", 1).Update(map[string]interface{}{"name": "Grace"}); err != nil || affected != 2 {
		t.Fatalf("事务更新失败: affected=%d err=%v", affected, err)
	}
	if affected, err := transaction.Table("users").WhereField("id", "=", 1).Inc("score", 2).Update(nil); err != nil || affected != 2 {
		t.Fatalf("事务表达式更新失败: affected=%d err=%v", affected, err)
	}
	if affected, err := transaction.Table("users").WhereField("id", "=", 1).Delete(); err != nil || affected != 2 {
		t.Fatalf("事务删除失败: affected=%d err=%v", affected, err)
	}
	if count, err := transaction.Table("users").WhereField("active", "=", true).Count(); err != nil || count != 1 {
		t.Fatalf("事务计数失败: count=%d err=%v", count, err)
	}
	if rows, err := transaction.Table("users").Join("profiles", "users.id = profiles.user_id").Select(); err != nil || len(rows) != 1 {
		t.Fatalf("事务高级查询失败: rows=%#v err=%v", rows, err)
	}
	if count, err := transaction.Table("users").Distinct().Count(); err != nil || count != 1 {
		t.Fatalf("事务高级计数失败: count=%d err=%v", count, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("提交完整事务失败: %v", err)
	}

	if err := database.TransactionContext(context.Background(), func(tx *Tx) error {
		_, err := tx.Name("users").WhereField("id", "=", 2).Select()
		return err
	}); err != nil {
		t.Fatalf("闭包事务成功路径失败: %v", err)
	}
}

// TestTransactionRollsBackAndRethrowsPanic 验证业务 panic 会先释放事务租约再原样抛出。
func TestTransactionRollsBackAndRethrowsPanic(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	marker := errors.New("panic marker")
	func() {
		defer func() {
			if recovered := recover(); recovered != marker {
				t.Fatalf("事务必须原样重新抛出 panic，实际为 %#v", recovered)
			}
		}()
		_ = database.Transaction(func(*Tx) error {
			panic(marker)
		})
	}()
	transaction, err := database.Begin()
	if err != nil {
		t.Fatalf("panic 回滚后连接租约应可再次使用: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("清理验证事务失败: %v", err)
	}
}

type rollbackFailureConnection struct{ rollbackErr error }

func (c *rollbackFailureConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("测试驱动不支持预编译语句")
}

func (c *rollbackFailureConnection) Close() error { return nil }

func (c *rollbackFailureConnection) Begin() (driver.Tx, error) {
	return &rollbackFailureTransaction{rollbackErr: c.rollbackErr}, nil
}

type rollbackFailureTransaction struct{ rollbackErr error }

func (t *rollbackFailureTransaction) Commit() error   { return nil }
func (t *rollbackFailureTransaction) Rollback() error { return t.rollbackErr }

// TestTransactionRejectsInvalidInputs 验证 nil 回调、上下文和依赖都以类型化错误返回。
func TestTransactionRejectsInvalidInputs(t *testing.T) {
	var unavailable *DB
	if _, err := unavailable.Begin(); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("nil DB Begin 应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}

	connection := newHardeningSQLConnection(t)
	database := NewDB(connection)
	if err := database.Transaction(nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("nil 事务回调应返回 ErrInvalidTransaction，实际为 %v", err)
	}
	// 类型化空上下文专用于覆盖异常分支，正常事务必须传递有效上下文。
	var nilContext context.Context
	if _, err := database.BeginTx(nilContext, nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("nil 事务上下文应返回 ErrInvalidTransaction，实际为 %v", err)
	}

	invalid := NewDB(&SQLConnection{Builder: &builder.Sqlite{}})
	if _, err := invalid.Begin(); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("缺失 sql.DB 应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
}

// TestTransactionJoinsCallbackAndRollbackErrors 验证回滚故障不会遮蔽或丢失业务错误。
func TestTransactionJoinsCallbackAndRollbackErrors(t *testing.T) {
	rollbackErr := errors.New("rollback backend failed")
	driverName := fmt.Sprintf("thinkgo_rollback_failure_%d", time.Now().UnixNano())
	sql.Register(driverName, &rollbackFailureDriver{rollbackErr: rollbackErr})
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("打开测试驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	database := NewDB(&SQLConnection{DB: handle, Builder: &builder.Sqlite{}})
	callbackErr := errors.New("business failed")
	err = database.Transaction(func(*Tx) error { return callbackErr })
	if !errors.Is(err, callbackErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("事务错误应同时包含业务与回滚错误，实际为 %v", err)
	}
}

// TestTransactionCompletionIsIdempotentlyRejected 验证事务结束后不会重复触碰底层句柄。
func TestTransactionCompletionIsIdempotentlyRejected(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	transaction, err := database.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}
	if err := transaction.Commit(); !errors.Is(err, ErrTransactionDone) {
		t.Fatalf("重复提交应返回 ErrTransactionDone，实际为 %v", err)
	}
	if err := transaction.Rollback(); !errors.Is(err, ErrTransactionDone) {
		t.Fatalf("提交后回滚应返回 ErrTransactionDone，实际为 %v", err)
	}
	if _, err := transaction.Table("users").Select(); !errors.Is(err, ErrTransactionDone) {
		t.Fatalf("事务结束后创建的查询应返回 ErrTransactionDone，实际为 %v", err)
	}
}

// TestDatabaseCloseWaitsForActiveTransaction 验证连接池不会在事务完成前关闭。
func TestDatabaseCloseWaitsForActiveTransaction(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	transaction, err := database.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- database.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("活动事务结束前 Close 不应返回，实际错误为 %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("回滚事务失败: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("关闭数据库失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("事务结束后数据库未完成关闭")
	}
}

// TestTransactionQueryDuringDatabaseCloseDoesNotDeadlock 验证 Close 等待活动事务时，
// 事务继续构造查询不会递归申请同一读锁而永久阻塞。
func TestTransactionQueryDuringDatabaseCloseDoesNotDeadlock(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	transaction, err := database.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- database.Close() }()
	waitForDatabaseCloseLock(t, database)

	queryDone := make(chan error, 1)
	go func() {
		_, queryErr := transaction.Table("users").Select()
		queryDone <- queryErr
	}()
	select {
	case queryErr := <-queryDone:
		if !errors.Is(queryErr, ErrDatabaseClosed) {
			t.Fatalf("关闭开始后的事务新查询应返回 ErrDatabaseClosed，实际为 %v", queryErr)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭等待活动事务时创建查询发生死锁")
	}

	if err = transaction.Rollback(); err != nil {
		t.Fatalf("回滚事务失败: %v", err)
	}
	select {
	case err = <-closeDone:
		if err != nil {
			t.Fatalf("数据库关闭失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("事务结束后数据库未完成关闭")
	}
}
