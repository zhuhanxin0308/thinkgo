package neo4j

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type healthNeo4jDriver struct {
	managedExecutorDriver
	deadline bool
}

func (driver *healthNeo4jDriver) NewSession(ctx context.Context, config neo4j.SessionConfig) neo4j.SessionWithContext {
	_, driver.deadline = ctx.Deadline()
	return driver.managedExecutorDriver.NewSession(ctx, config)
}

// TestNeo4jPingContract 验证探针调用正式连接能力并保留操作预算、取消和驱动失败。
func TestNeo4jPingContract(t *testing.T) {
	result := &managedExecutorResult{record: &neo4j.Record{Keys: []string{"thinkgo_probe"}, Values: []interface{}{int64(1)}}}
	session := &managedExecutorSession{result: result}
	driver := &healthNeo4jDriver{managedExecutorDriver: managedExecutorDriver{session: session}}
	connection := &Neo4jConnection{Driver: driver, Database: "tenant_a", OperationTimeout: time.Second}
	if err := connection.PingContext(context.Background()); err != nil || !driver.deadline {
		t.Fatalf("探针缺少操作预算或错误失败: deadline=%t err=%v", driver.deadline, err)
	}
	if driver.lastConfig.DatabaseName != "tenant_a" || driver.lastConfig.AccessMode != neo4j.AccessModeRead ||
		session.lastCypher != "RETURN 1 AS thinkgo_probe" || session.closeCalls != 1 {
		t.Fatalf("探针未使用目标库的只读事务或未关闭会话: %#v", driver.lastConfig)
	}
	result.record.Values[0] = int64(0)
	if err := connection.PingContext(context.Background()); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("异常探针值应返回不可用错误: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := connection.PingContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("探针取消原因丢失: %v", err)
	}
	//lint:ignore SA1012 本例明确验证驱动拒绝空探针上下文。
	if err := connection.PingContext(nil); err == nil {
		t.Fatal("空上下文应失败")
	}
	for _, unavailable := range []*Neo4jConnection{nil, {}, {executor: &fakeNeo4jExecutor{}}} {
		if err := unavailable.PingContext(context.Background()); !errors.Is(err, ErrDatabaseUnavailable) {
			t.Fatalf("缺少真实驱动不应报告健康: %v", err)
		}
	}
}
