package framework

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
)

var databaseExitTestSequence atomic.Uint64

type initializationExitConnector struct {
	connect func(db.Config) (db.Connection, error)
}

func (connector *initializationExitConnector) Connect(config db.Config) (db.Connection, error) {
	return connector.connect(config)
}

func registerInitializationExitConnector(t *testing.T, connect func(db.Config) (db.Connection, error)) string {
	t.Helper()
	name := fmt.Sprintf("initialization_exit_%d", databaseExitTestSequence.Add(1))
	if err := db.RegisterConnector(name, &initializationExitConnector{connect: connect}); err != nil {
		t.Fatal(err)
	}
	return name
}

// 每个配置都必须发布终结结果；全部连接器退出也不能耗尽仍有待处理任务的 worker。
func TestDatabaseInitializationReportsExitedConnectors(t *testing.T) {
	for _, count := range []int{1, maxDatabaseInitializationConcurrency + 3} {
		t.Run(fmt.Sprintf("connections_%d", count), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				connector := registerInitializationExitConnector(t, func(db.Config) (db.Connection, error) {
					runtime.Goexit()
					return nil, nil
				})
				specs := make([]databaseConnectionSpec, count)
				for index := range specs {
					specs[index] = databaseConnectionSpec{
						name:   fmt.Sprintf("connection_%d", index),
						config: db.Config{Type: connector},
					}
				}
				done := make(chan map[string]databaseConnectionResult, 1)
				go func() { done <- initializeDatabaseConnections(specs) }()
				synctest.Wait()
				select {
				case results := <-done:
					if len(results) != count {
						t.Fatalf("配置有 %d 项，仅发布 %d 项结果", count, len(results))
					}
					for _, spec := range specs {
						result := results[spec.name]
						if result.name != spec.name || result.database != nil || result.err == nil {
							t.Fatalf("退出的连接器未报告对应失败：%+v", result)
						}
					}
				default:
					t.Fatal("连接器退出后，未处理配置仍阻塞初始化")
				}
			})
		})
	}
}

// 默认连接异常退出必须走既有策略分支：required 阻断启动，degraded 保留自动重试工厂。
func TestDatabaseInitializationExitPreservesStartupPolicy(t *testing.T) {
	for _, policy := range []DatabaseStartupPolicy{DatabaseStartupRequired, DatabaseStartupDegraded} {
		t.Run(string(policy), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				connector := registerInitializationExitConnector(t, func(db.Config) (db.Connection, error) {
					if calls.Add(1) == 1 {
						runtime.Goexit()
					}
					return &appFakeManagerConnection{identity: db.NewConnectionID("recovered")}, nil
				})
				app := &App{
					dbManager:             db.NewManager("primary"),
					env:                   env.NewEnv(),
					databaseStartupPolicy: policy,
				}
				app.initDatabaseConnections(map[string]interface{}{
					"connections": map[string]interface{}{
						"primary": map[string]interface{}{"type": connector},
					},
				}, "primary")
				if app.db != nil || app.databaseReadinessError() == nil {
					t.Fatal("失败的默认连接被报告为就绪")
				}
				connection, err := app.dbManager.Default()
				if policy == DatabaseStartupRequired {
					if app.StartupError() == nil || !errors.Is(err, db.ErrConnectionNotFound) || calls.Load() != 1 {
						t.Fatalf("required 启动失败语义改变：启动错误=%v，连接错误=%v，次数=%d", app.StartupError(), err, calls.Load())
					}
				} else if app.StartupError() != nil || err != nil || connection == nil || calls.Load() != 2 || app.databaseReadinessError() != nil {
					t.Fatalf("degraded 未恢复默认连接：启动错误=%v，连接错误=%v，次数=%d", app.StartupError(), err, calls.Load())
				}
				if err := app.dbManager.Close(); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

type initializationOwnedConnection struct {
	appFakeManagerConnection
	closed atomic.Int32
}

func (connection *initializationOwnedConnection) Close() error {
	connection.closed.Add(1)
	return nil
}

// 同一批中的异常不能取消成功连接，也不能抹去普通错误或兼容 nil panic 的诊断。
func TestDatabaseInitializationMixedOutcomes(t *testing.T) {
	t.Setenv("GODEBUG", "panicnil=1")
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("连接暂不可用")
		backend := &initializationOwnedConnection{
			appFakeManagerConnection: appFakeManagerConnection{identity: db.NewConnectionID("mixed")},
		}
		connector := registerInitializationExitConnector(t, func(config db.Config) (db.Connection, error) {
			switch config.Database {
			case "error":
				return nil, failure
			case "panic":
				panic("连接器异常")
			case "nilpanic":
				panic(nil)
			case "exit":
				runtime.Goexit()
			}
			return backend, nil
		})
		names := []string{"success", "error", "panic", "nilpanic", "exit"}
		specs := make([]databaseConnectionSpec, len(names))
		for index, name := range names {
			specs[index] = databaseConnectionSpec{name: name, config: db.Config{Type: connector, Database: name}}
		}
		results := initializeDatabaseConnections(specs)
		if len(results) != len(names) {
			t.Fatalf("混合初始化丢失结果：%d", len(results))
		}
		if result := results["error"]; !errors.Is(result.err, failure) || result.database != nil {
			t.Fatalf("普通连接错误被覆盖：%+v", result)
		}
		for _, name := range []string{"panic", "nilpanic"} {
			result := results[name]
			if result.database != nil || result.err == nil || !strings.Contains(result.err.Error(), "panic:") {
				t.Fatalf("异常诊断丢失：%s，%+v", name, result)
			}
		}
		if result := results["exit"]; result.database != nil || !errors.Is(result.err, db.ErrDatabaseUnavailable) {
			t.Fatalf("异常退出未报告连接不可用：%+v", result)
		}
		success := results["success"]
		if success.err != nil || success.database == nil || backend.closed.Load() != 0 {
			t.Fatalf("成功连接被丢弃或提前关闭：%+v", success)
		}
		if err := success.database.Close(); err != nil {
			t.Fatal(err)
		}
		if backend.closed.Load() != 1 {
			t.Fatalf("成功连接关闭了 %d 次", backend.closed.Load())
		}
	})
}
