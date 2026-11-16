package neo4j

import (
	"context"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type timeoutCapturingNeoDriver struct {
	managedExecutorDriver
	deadline time.Time
}

func (driver *timeoutCapturingNeoDriver) NewSession(ctx context.Context, config neo4j.SessionConfig) neo4j.SessionWithContext {
	driver.deadline, _ = ctx.Deadline()
	return driver.managedExecutorDriver.NewSession(ctx, config)
}

// TestNeoQueryUsesConfiguredDefaultAndCallerDeadline 验证 ORM 默认预算可以被驱动配置替换，显式期限原样传递。
func TestNeoQueryUsesConfiguredDefaultAndCallerDeadline(t *testing.T) {
	const configuredTimeout = 52 * time.Second
	session := &managedExecutorSession{result: &managedExecutorResult{records: []*neo4j.Record{}}}
	driver := &timeoutCapturingNeoDriver{managedExecutorDriver: managedExecutorDriver{session: session}}
	connection := &Neo4jConnection{Driver: driver, Database: "app", OperationTimeout: configuredTimeout}
	database := db.NewDB(connection)
	query := database.Table("users")
	before := time.Now()
	if _, err := query.Select(); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if driver.deadline.Before(before.Add(configuredTimeout)) || driver.deadline.After(after.Add(configuredTimeout)) {
		t.Errorf("缺省操作应使用配置的预算: %v", driver.deadline.Sub(before))
	}
	for _, timeout := range []time.Duration{time.Second, 2 * configuredTimeout} {
		want := time.Now().Add(timeout)
		ctx, cancel := context.WithDeadline(t.Context(), want)
		_, err := query.WithContext(ctx).Select()
		cancel()
		if err != nil || !driver.deadline.Equal(want) {
			t.Errorf("显式期限必须原样传递，期望 %v，实际 %v，错误 %v", want, driver.deadline, err)
		}
	}
}
