package framework

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/env"
)

// parallelDatabaseConnector 在所有连接器进入后才放行，用于证明初始化不会被首个连接串行阻塞。
type parallelDatabaseConnector struct {
	entered *atomic.Int32
	release <-chan struct{}
}

func (c *parallelDatabaseConnector) Connect(db.Config) (db.Connection, error) {
	c.entered.Add(1)
	<-c.release
	return &appFakeManagerConnection{identity: db.NewConnectionID("parallel-test")}, nil
}

// TestInitDatabaseConnectionsUsesBoundedParallelism 验证多个数据库连接会并行初始化，
// 同时仍将连接结果按稳定名称顺序安装，保持默认连接和管理器语义不变。
func TestInitDatabaseConnectionsUsesBoundedParallelism(t *testing.T) {
	connectorName := fmt.Sprintf("parallel_init_%d", time.Now().UnixNano())
	release := make(chan struct{})
	var entered atomic.Int32
	if err := db.RegisterConnector(connectorName, &parallelDatabaseConnector{
		entered: &entered,
		release: release,
	}); err != nil {
		t.Fatalf("注册并行测试连接器失败: %v", err)
	}

	const connectionCount = 4
	connections := make(map[string]interface{}, connectionCount)
	for index := 0; index < connectionCount; index++ {
		name := fmt.Sprintf("connection_%d", index)
		connections[name] = map[string]interface{}{
			"type":     connectorName,
			"database": name,
		}
	}
	app := &App{
		dbManager: db.NewManager("connection_0"),
		env:       env.NewEnv(),
	}
	databaseConfig := map[string]interface{}{"connections": connections}
	done := make(chan struct{})
	go func() {
		app.initDatabaseConnections(databaseConfig, "connection_0")
		close(done)
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for entered.Load() < connectionCount {
		select {
		case <-deadline.C:
			enteredBeforeRelease := entered.Load()
			close(release)
			<-done
			t.Fatalf("数据库连接初始化仍按串行路径执行，释放前仅进入 %d/%d 个连接器", enteredBeforeRelease, connectionCount)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("并行数据库连接初始化未完成")
	}
	for name := range connections {
		if connection, err := app.dbManager.Connection(name); err != nil || connection == nil {
			t.Fatalf("连接 %q 未安装到数据库管理器: connection=%v err=%v", name, connection, err)
		}
	}
	if app.db == nil {
		t.Fatal("默认数据库连接应绑定到 ServiceDB")
	}
}

// TestInitDatabaseConnectionsCapsWorkerCount 验证连接初始化不会为每个配置项无限创建 goroutine。
func TestInitDatabaseConnectionsCapsWorkerCount(t *testing.T) {
	connectorName := fmt.Sprintf("parallel_limit_%d", time.Now().UnixNano())
	release := make(chan struct{})
	var entered atomic.Int32
	if err := db.RegisterConnector(connectorName, &parallelDatabaseConnector{
		entered: &entered,
		release: release,
	}); err != nil {
		t.Fatalf("注册并发上限测试连接器失败: %v", err)
	}

	const connectionCount = maxDatabaseInitializationConcurrency * 2
	connections := make(map[string]interface{}, connectionCount)
	for index := 0; index < connectionCount; index++ {
		name := fmt.Sprintf("connection_%d", index)
		connections[name] = map[string]interface{}{
			"type":     connectorName,
			"database": name,
		}
	}
	app := &App{dbManager: db.NewManager("connection_0"), env: env.NewEnv()}
	done := make(chan struct{})
	go func() {
		app.initDatabaseConnections(map[string]interface{}{"connections": connections}, "connection_0")
		close(done)
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for entered.Load() < maxDatabaseInitializationConcurrency {
		select {
		case <-deadline.C:
			close(release)
			<-done
			t.Fatalf("并行初始化未达到 worker 上限，已进入 %d 个连接器", entered.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(25 * time.Millisecond)
	if got := entered.Load(); got != maxDatabaseInitializationConcurrency {
		close(release)
		<-done
		t.Fatalf("数据库初始化 worker 数量未受限，释放前进入 %d 个连接器，期望 %d", got, maxDatabaseInitializationConcurrency)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("达到并发上限后数据库初始化未完成")
	}
}

type panicDatabaseConnector struct{}

func (*panicDatabaseConnector) Connect(db.Config) (db.Connection, error) {
	panic("connector panic marker")
}

// TestConnectDatabaseSafelyConvertsConnectorPanic 验证第三方连接器异常不会越过 worker 崩溃宿主进程。
func TestConnectDatabaseSafelyConvertsConnectorPanic(t *testing.T) {
	connectorName := fmt.Sprintf("panic_init_%d", time.Now().UnixNano())
	if err := db.RegisterConnector(connectorName, &panicDatabaseConnector{}); err != nil {
		t.Fatalf("注册 panic 测试连接器失败: %v", err)
	}
	app := &App{dbManager: db.NewManager("primary"), env: env.NewEnv()}
	app.initDatabaseConnections(map[string]interface{}{
		"connections": map[string]interface{}{
			"primary": map[string]interface{}{"type": connectorName, "database": "test"},
		},
	}, "primary")
	if app.db != nil {
		t.Fatal("连接器 panic 后不应安装数据库连接")
	}
	if app.StartupError() != nil {
		t.Fatalf("连接器 panic 属于可恢复连接失败，不应升级为启动错误: %v", app.StartupError())
	}
	if _, err := connectDatabaseSafely(db.Config{Type: connectorName}); err == nil || !strings.Contains(err.Error(), "connector panic marker") {
		t.Fatalf("连接器 panic 应转换为可观察错误，实际为 %v", err)
	}
}
