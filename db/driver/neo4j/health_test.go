package neo4j

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type healthNeo4jDriver struct {
	neo4j.DriverWithContext
	failure  error
	deadline bool
}

func (driver *healthNeo4jDriver) VerifyConnectivity(ctx context.Context) error {
	_, driver.deadline = ctx.Deadline()
	if err := ctx.Err(); err != nil {
		return err
	}
	return driver.failure
}

// TestNeo4jPingContract 验证探针调用正式连接能力并保留操作预算、取消和驱动失败。
func TestNeo4jPingContract(t *testing.T) {
	driver := &healthNeo4jDriver{}
	connection := &Neo4jConnection{Driver: driver, OperationTimeout: time.Second}
	if err := connection.PingContext(context.Background()); err != nil || !driver.deadline {
		t.Fatalf("探针缺少操作预算或错误失败: deadline=%t err=%v", driver.deadline, err)
	}
	driver.failure = errors.New("节点不可达")
	if err := connection.PingContext(context.Background()); !errors.Is(err, driver.failure) {
		t.Fatalf("连接失败原因丢失: %v", err)
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
