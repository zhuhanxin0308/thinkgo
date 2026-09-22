package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

const statementConcurrencyDeadline = time.Second

// TestSQLColdStatementsExecuteConcurrently 验证两个未预热的 SQL 不会被缓存写锁串行执行。
func TestSQLColdStatementsExecuteConcurrently(t *testing.T) {
	connection, state := newConcurrentStatementCacheConnection(t)
	state.queryStarted = make(chan struct{}, 2)
	state.queryRelease = make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(state.queryRelease) })
	results := make(chan error, 2)
	for _, query := range []string{"SELECT value FROM first_cold", "SELECT value FROM second_cold"} {
		go func() {
			_, err := connection.Query(query)
			results <- err
		}()
	}
	for range 2 {
		select {
		case <-state.queryStarted:
		case <-time.After(statementConcurrencyDeadline):
			t.Fatal("不同冷语句必须同时进入数据库，缓存锁不得覆盖数据库执行")
		}
	}
	release.Do(func() { close(state.queryRelease) })
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("并发冷查询失败: %v", err)
		}
	}
}

// TestSQLPreparingStatementDoesNotBlockCachedQuery 验证慢预编译不阻塞其他已缓存语句。
func TestSQLPreparingStatementDoesNotBlockCachedQuery(t *testing.T) {
	connection, state := newConcurrentStatementCacheConnection(t)
	const cachedQuery = "SELECT value FROM ready_rows"
	if _, err := connection.Query(cachedQuery); err != nil {
		t.Fatal(err)
	}
	state.prepareQuery = "SELECT value FROM preparing_rows"
	state.prepareStarted = make(chan struct{}, 1)
	state.prepareRelease = make(chan struct{})
	defer close(state.prepareRelease)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coldResult := make(chan error, 1)
	go func() {
		_, err := connection.QueryContext(ctx, state.prepareQuery)
		coldResult <- err
	}()
	select {
	case <-state.prepareStarted:
	case <-time.After(statementConcurrencyDeadline):
		t.Fatal("未进入受控预编译")
	}
	readyResult := make(chan error, 1)
	go func() {
		_, err := connection.Query(cachedQuery)
		readyResult <- err
	}()
	select {
	case err := <-readyResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(statementConcurrencyDeadline):
		t.Fatal("慢预编译阻塞了无关缓存命中")
	}
	cancel()
	if err := <-coldResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("预编译必须保留取消原因: %v", err)
	}
}

// TestSQLWaitingForSamePreparationCanCancel 验证等待同一冷语句的调用有独立取消边界。
func TestSQLWaitingForSamePreparationCanCancel(t *testing.T) {
	connection, state := newConcurrentStatementCacheConnection(t)
	state.prepareQuery = "SELECT value FROM shared_preparation"
	state.prepareStarted = make(chan struct{}, 2)
	state.prepareRelease = make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(state.prepareRelease) })
	first := make(chan error, 1)
	go func() {
		_, err := connection.Query(state.prepareQuery)
		first <- err
	}()
	select {
	case <-state.prepareStarted:
	case <-time.After(statementConcurrencyDeadline):
		t.Fatal("未进入受控预编译")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := connection.QueryContext(ctx, state.prepareQuery)
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("等待预编译的取消原因丢失: %v", err)
		}
	case <-time.After(statementConcurrencyDeadline):
		t.Fatal("等待同一语句预编译时无法及时取消")
	}
	release.Do(func() { close(state.prepareRelease) })
	if err := <-first; err != nil {
		t.Fatalf("等待者取消不得取消首个调用: %v", err)
	}
}

// TestSQLPreparingEntrySurvivesEviction 验证淘汰仍在准备的语句不会关闭其在途租约。
func TestSQLPreparingEntrySurvivesEviction(t *testing.T) {
	connection, state := newConcurrentStatementCacheConnection(t)
	connection.statementCapacity = 1
	state.prepareQuery = "SELECT value FROM evicted_preparation"
	state.prepareStarted = make(chan struct{}, 1)
	state.prepareRelease = make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(state.prepareRelease) })
	first := make(chan error, 1)
	go func() {
		_, err := connection.Query(state.prepareQuery)
		first <- err
	}()
	select {
	case <-state.prepareStarted:
	case <-time.After(statementConcurrencyDeadline):
		t.Fatal("未进入受控预编译")
	}
	if _, err := connection.Execute("UPDATE eviction_rows SET value = ?", 1); err != nil {
		t.Fatal(err)
	}
	if state.closes.Load() != 0 {
		t.Fatal("预编译中的租约被提前关闭")
	}
	release.Do(func() { close(state.prepareRelease) })
	if err := <-first; err != nil {
		t.Fatalf("被淘汰语句的在途查询失败: %v", err)
	}
	if state.closes.Load() != 1 {
		t.Fatal("被淘汰语句在租约释放后未回收")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if state.closes.Load() != state.prepares.Load() {
		t.Fatal("连接关闭后仍有未释放的预编译语句")
	}
}

// TestSQLCloseWaitsForActiveLease 验证关闭等待在途租约，拒绝新租约并向所有关闭者返回完整结果。
func TestSQLCloseWaitsForActiveLease(t *testing.T) {
	connection, state := newConcurrentStatementCacheConnection(t)
	entry, err := connection.acquireStatement(context.Background(), "SELECT value FROM closing_rows")
	if err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	defer release.Do(func() { connection.releaseStatement(entry) })
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- connection.closeStatements() }()
	}
	deadline := time.After(statementConcurrencyDeadline)
	for {
		connection.statementMu.RLock()
		sealed := connection.statementsClosed
		connection.statementMu.RUnlock()
		if sealed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("关闭未封闭新语句租约")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := connection.acquireStatement(context.Background(), "SELECT value FROM late_rows"); !errors.Is(err, ErrDatabaseClosed) {
		t.Fatalf("关闭开始后仍允许创建语句: %v", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("租约释放前关闭提前返回: %v", err)
	default:
	}
	release.Do(func() { connection.releaseStatement(entry) })
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(statementConcurrencyDeadline):
			t.Fatal("关闭未在租约释放后完成")
		}
	}
	if state.closes.Load() != 1 {
		t.Fatalf("并发关闭未准确释放一次语句: %d", state.closes.Load())
	}
}

// TestSQLCreatorCancellationDoesNotCancelOtherRequests 验证合并预编译不会传播其他请求的取消。
func TestSQLCreatorCancellationDoesNotCancelOtherRequests(t *testing.T) {
	connection, state := newConcurrentStatementCacheConnection(t)
	state.prepareQuery = "SELECT value FROM canceled_creator"
	state.prepareStarted = make(chan struct{}, 2)
	state.prepareRelease = make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(state.prepareRelease) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creator := make(chan error, 1)
	go func() {
		_, err := connection.QueryContext(ctx, state.prepareQuery)
		creator <- err
	}()
	select {
	case <-state.prepareStarted:
	case <-time.After(statementConcurrencyDeadline):
		t.Fatal("首个请求未进入预编译")
	}
	waiter := make(chan error, 1)
	go func() {
		_, err := connection.Query(state.prepareQuery)
		waiter <- err
	}()
	deadline := time.After(statementConcurrencyDeadline)
	for {
		connection.statementMu.RLock()
		users := connection.statements[state.prepareQuery].users
		connection.statementMu.RUnlock()
		if users == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("第二个请求未加入预编译等待")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-creator; !errors.Is(err, context.Canceled) {
		t.Fatalf("首个请求没有返回自身取消原因: %v", err)
	}
	release.Do(func() { close(state.prepareRelease) })
	select {
	case err := <-waiter:
		if err != nil {
			t.Fatalf("有效请求不应继承首个请求的取消: %v", err)
		}
	case <-time.After(statementConcurrencyDeadline):
		t.Fatal("有效请求未重新完成预编译")
	}
}
