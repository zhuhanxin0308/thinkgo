package db

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

type lifecycleConnector struct{ connection Connection }

func (c *lifecycleConnector) Connect(Config) (Connection, error) { return c.connection, nil }

type configCaptureConnector struct {
	mu        sync.Mutex
	callCount int
}

func (c *configCaptureConnector) Connect(config Config) (Connection, error) {
	c.mu.Lock()
	c.callCount++
	c.mu.Unlock()
	config.Params["driver_mutation"] = "isolated"
	return &mockConnection{}, nil
}

func (c *configCaptureConnector) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callCount
}

type lifecycleConnection struct {
	mockConnection
	mu         sync.Mutex
	closeCount int
	closeErr   error
}

type blockingLifecycleConnection struct {
	mockConnection
	started   chan struct{}
	release   chan struct{}
	closed    chan struct{}
	selectErr error
	startOnce sync.Once
	closeOnce sync.Once
}

func (c *blockingLifecycleConnection) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	c.startOnce.Do(func() { close(c.started) })
	<-c.release
	return nil, c.selectErr
}

func (c *blockingLifecycleConnection) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *lifecycleConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCount++
	return c.closeErr
}

func (c *lifecycleConnection) closedTimes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}

// waitForDatabaseCloseLock 等待 Close 已设置关闭状态或已排队等待写锁，
// 避免通过固定休眠猜测并发时序，使死锁回归测试在不同负载下保持稳定。
func waitForDatabaseCloseLock(t *testing.T, database *DB) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !database.mu.TryRLock() {
			return
		}
		closed := database.closed
		database.mu.RUnlock()
		if closed {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("Close 未进入关闭状态或写锁等待状态")
}

// TestConnectorRegistryRejectsInvalidAndDuplicateEntries 验证全局注册表不会静默覆盖或保存类型化 nil。
func TestConnectorRegistryRejectsInvalidAndDuplicateEntries(t *testing.T) {
	name := fmt.Sprintf("hardening_lifecycle_connector_%d", time.Now().UnixNano())
	connector := &lifecycleConnector{connection: &mockConnection{}}
	if err := RegisterConnector(name, connector); err != nil {
		t.Fatalf("注册合法连接器失败: %v", err)
	}
	if err := RegisterConnector(name, &lifecycleConnector{}); !errors.Is(err, ErrDuplicateConnector) {
		t.Fatalf("重复连接器应返回 ErrDuplicateConnector，实际为 %v", err)
	}
	if err := RegisterConnector("bad name", connector); !errors.Is(err, ErrInvalidConnector) {
		t.Fatalf("非法名称应返回 ErrInvalidConnector，实际为 %v", err)
	}
	var typedNil *lifecycleConnector
	if err := RegisterConnector(name+"_typed_nil", typedNil); !errors.Is(err, ErrInvalidConnector) {
		t.Fatalf("类型化 nil 连接器应返回 ErrInvalidConnector，实际为 %v", err)
	}
	resolved, err := GetConnector(name)
	if err != nil || resolved != connector {
		t.Fatalf("读取已注册连接器失败: connector=%#v err=%v", resolved, err)
	}
	if _, err = GetConnector("missing_hardening_connector"); !errors.Is(err, ErrConnectorNotFound) {
		t.Fatalf("缺失连接器应返回 ErrConnectorNotFound，实际为 %v", err)
	}
}

// TestDatabaseRejectsNilConnectionAndClosesOnce 验证依赖错误显式传播且并发关闭只执行一次。
func TestDatabaseRejectsNilConnectionAndClosesOnce(t *testing.T) {
	var typedNil *lifecycleConnection
	database := NewDB(typedNil)
	if _, err := database.Table("users").Select(); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("类型化 nil 连接应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
	if _, err := database.Query("SELECT 1"); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("nil 连接原生查询应失败，实际为 %v", err)
	}
	if err := database.WithConnection(func(Connection) error { return nil }); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("WithConnection 应显式返回依赖错误，实际为 %v", err)
	}

	backendErr := errors.New("close failed")
	connection := &lifecycleConnection{closeErr: backendErr}
	database = NewDB(connection)
	const workers = 20
	var wg sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsChannel <- database.Close()
		}()
	}
	wg.Wait()
	close(errorsChannel)
	for closeErr := range errorsChannel {
		if !errors.Is(closeErr, backendErr) {
			t.Fatalf("所有 Close 调用都应看到同一错误，实际为 %v", closeErr)
		}
	}
	if connection.closedTimes() != 1 {
		t.Fatalf("底层连接应只关闭一次，实际为 %d", connection.closedTimes())
	}
	if _, err := database.Table("users").Select(); !errors.Is(err, ErrDatabaseClosed) {
		t.Fatalf("关闭后查询应返回 ErrDatabaseClosed，实际为 %v", err)
	}
}

func TestWithConnectionHoldsLeaseUntilCallbackReturns(t *testing.T) {
	database := NewDB(&lifecycleConnection{})
	entered := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- database.WithConnection(func(Connection) error {
			close(entered)
			<-releaseCallback
			return nil
		})
	}()
	<-entered

	closed := make(chan error, 1)
	go func() { closed <- database.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before borrowed callback completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCallback)
	if err := <-callbackDone; err != nil {
		t.Fatalf("connection callback failed: %v", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close failed after callback: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback lease was not released")
	}
}

// TestDatabaseCloseContextCanCancelLeaseWait 验证数据库关闭等待租约时可由上下文取消，并支持后续重试关闭。
func TestDatabaseCloseContextCanCancelLeaseWait(t *testing.T) {
	connection := &lifecycleConnection{}
	database := NewDB(connection)
	entered := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- database.WithConnection(func(Connection) error {
			close(entered)
			<-releaseCallback
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := database.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("关闭上下文超时应返回 DeadlineExceeded，实际为 %v", err)
	}
	if connection.closedTimes() != 0 {
		t.Fatal("关闭等待超时期间不得关闭仍有在途租约的物理连接")
	}
	close(releaseCallback)
	if err := <-callbackDone; err != nil {
		t.Fatalf("释放数据库租约失败: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("租约释放后重试关闭失败: %v", err)
	}
	if connection.closedTimes() != 1 {
		t.Fatalf("物理连接应只关闭一次，实际为 %d", connection.closedTimes())
	}
}

// TestDatabaseCloseContextRejectsNil 验证关闭上下文不能静默接受 nil。
func TestDatabaseCloseContextRejectsNil(t *testing.T) {
	database := NewDB(&lifecycleConnection{})
	var ctx context.Context
	if err := database.CloseContext(ctx); !errors.Is(err, ErrInvalidDatabaseContext) {
		t.Fatalf("nil 关闭上下文应返回 ErrInvalidDatabaseContext，实际为 %v", err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := database.CloseContext(canceledContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消关闭上下文应返回 context.Canceled，实际为 %v", err)
	}
}

func TestWithConnectionReleasesLeaseAfterPanic(t *testing.T) {
	database := NewDB(&lifecycleConnection{})
	func() {
		defer func() {
			if recovered := recover(); recovered != "boom" {
				t.Fatalf("unexpected panic: %#v", recovered)
			}
		}()
		_ = database.WithConnection(func(Connection) error { panic("boom") })
	}()
	if err := database.Close(); err != nil {
		t.Fatalf("panic must release connection lease: %v", err)
	}
}

// TestManagerClosesEveryUniqueConnectionAndFreezesRegistry 验证关闭聚合全部错误且拒绝后续注册。
func TestManagerClosesEveryUniqueConnectionAndFreezesRegistry(t *testing.T) {
	firstErr := errors.New("first close")
	secondErr := errors.New("second close")
	firstBackend := &lifecycleConnection{closeErr: firstErr}
	secondBackend := &lifecycleConnection{closeErr: secondErr}
	first := NewDB(firstBackend)
	second := NewDB(secondBackend)
	manager := NewManager("primary")
	var unavailableBackend *lifecycleConnection
	if err := manager.Add("unavailable", NewDB(unavailableBackend)); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("Manager 应拒绝不可用连接，实际为 %v", err)
	}
	if err := manager.Add("primary", first); err != nil {
		t.Fatalf("注册默认连接失败: %v", err)
	}
	if err := manager.Add("alias", first); err != nil {
		t.Fatalf("注册连接别名失败: %v", err)
	}
	if err := manager.Add("secondary", second); err != nil {
		t.Fatalf("注册第二连接失败: %v", err)
	}
	if err := manager.Add("primary", second); !errors.Is(err, ErrDuplicateConnection) {
		t.Fatalf("覆盖已有名称应返回 ErrDuplicateConnection，实际为 %v", err)
	}
	defaultDB, err := manager.Default()
	if err != nil || defaultDB != first {
		t.Fatalf("读取默认连接失败: db=%#v err=%v", defaultDB, err)
	}
	closeErr := manager.Close()
	if !errors.Is(closeErr, firstErr) || !errors.Is(closeErr, secondErr) {
		t.Fatalf("Close 应聚合所有连接错误，实际为 %v", closeErr)
	}
	if firstBackend.closedTimes() != 1 || secondBackend.closedTimes() != 1 {
		t.Fatalf("别名连接应去重关闭: first=%d second=%d", firstBackend.closedTimes(), secondBackend.closedTimes())
	}
	if err = manager.Add("late", NewDB(&mockConnection{})); !errors.Is(err, ErrDatabaseManagerClosed) {
		t.Fatalf("关闭后 Add 应返回 ErrDatabaseManagerClosed，实际为 %v", err)
	}
	if _, err = manager.Connection("primary"); !errors.Is(err, ErrDatabaseManagerClosed) {
		t.Fatalf("关闭后读取连接应返回 ErrDatabaseManagerClosed，实际为 %v", err)
	}
	if err = manager.Close(); !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("重复 Close 应返回稳定聚合错误，实际为 %v", err)
	}
}

// TestAutoTimestampRejectsNarrowIntegerOverflow 验证自动时间戳不会向窄整数静默截断。
func TestAutoTimestampRejectsNarrowIntegerOverflow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	data := map[string]interface{}{"created_at": int8(0)}
	if err := setAutoTimestamp(data, "created_at", now, TimestampValueTypeUnix); !errors.Is(err, ErrTimestampOverflow) {
		t.Fatalf("int8 时间戳应返回 ErrTimestampOverflow，实际为 %v", err)
	}
	if data["created_at"] != int8(0) {
		t.Fatalf("溢出失败不得修改原值，实际为 %#v", data["created_at"])
	}
	data["created_at"] = int64(0)
	if err := setAutoTimestamp(data, "created_at", now, TimestampValueTypeUnix); err != nil {
		t.Fatalf("int64 时间戳写入失败: %v", err)
	}
	if data["created_at"] != now.Unix() {
		t.Fatalf("int64 时间戳错误: %#v", data["created_at"])
	}
}

// TestDatabaseCloseWaitsForInFlightQuery 验证关闭不会截断正在使用底层连接的查询。
func TestDatabaseCloseWaitsForInFlightQuery(t *testing.T) {
	connection := &blockingLifecycleConnection{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	database := NewDB(connection)
	queryDone := make(chan error, 1)
	go func() {
		_, err := database.Table("users").Select()
		queryDone <- err
	}()
	<-connection.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- database.Close() }()
	select {
	case <-connection.closed:
		t.Fatal("仍有查询执行时不得关闭底层连接")
	case <-time.After(50 * time.Millisecond):
	}

	close(connection.release)
	if err := <-queryDone; err != nil {
		t.Fatalf("在途查询失败: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	select {
	case <-connection.closed:
	case <-time.After(time.Second):
		t.Fatal("查询结束后底层连接未关闭")
	}
}

// TestDatabaseCloseDoesNotDeadlockFailedQueryReporting 验证关闭等待在途查询时，
// 查询错误仍能完成日志上报、释放租约并让底层连接安全关闭。
func TestDatabaseCloseDoesNotDeadlockFailedQueryReporting(t *testing.T) {
	backendErr := errors.New("query failed during close")
	connection := &blockingLifecycleConnection{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		closed:    make(chan struct{}),
		selectErr: backendErr,
	}
	database := NewDB(connection)
	queryDone := make(chan error, 1)
	go func() {
		_, err := database.Table("users").Select()
		queryDone <- err
	}()
	<-connection.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- database.Close() }()
	waitForDatabaseCloseLock(t, database)
	close(connection.release)

	select {
	case err := <-queryDone:
		if !errors.Is(err, backendErr) {
			t.Fatalf("查询应返回原始后端错误，实际为 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("查询错误上报与数据库关闭发生死锁")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("数据库关闭失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("失败查询释放后数据库未完成关闭")
	}
}

// TestConnectValidatesAndCopiesConfiguration 验证非法配置不会触达驱动，合法 map 也不会被驱动反向修改。
func TestConnectValidatesAndCopiesConfiguration(t *testing.T) {
	name := fmt.Sprintf("hardening_config_connector_%d", time.Now().UnixNano())
	connector := &configCaptureConnector{}
	if err := RegisterConnector(name, connector); err != nil {
		t.Fatalf("注册测试连接器失败: %v", err)
	}
	invalidConfigs := []Config{
		{Type: name, MaxOpenConns: -1},
		{Type: name, MaxOpenConns: 1, MaxIdleConns: 2},
		{Type: name, TimestampValueType: "unknown"},
		{Type: name, Prefix: "bad prefix"},
		{Type: name, Params: map[string]string{"": "value"}},
		{Type: name, Params: map[string]string{" tls ": "false"}},
	}
	for index, config := range invalidConfigs {
		if _, err := Connect(config); !errors.Is(err, ErrInvalidDatabaseConfig) {
			t.Fatalf("第 %d 个非法配置应返回 ErrInvalidDatabaseConfig，实际为 %v", index, err)
		}
	}
	if connector.calls() != 0 {
		t.Fatalf("非法配置不应调用连接器，实际调用 %d 次", connector.calls())
	}

	params := map[string]string{"charset": "utf8mb4"}
	database, err := Connect(Config{Type: name, Params: params})
	if err != nil {
		t.Fatalf("合法配置连接失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, mutated := params["driver_mutation"]; mutated {
		t.Fatal("连接器不得修改调用方 Params map")
	}
}
