package mongo

import (
	"context"
	"errors"
	"testing"
)

// TestMongoPingContract 使用正式驱动的模拟部署验证真实 ping 命令及错误传播。
func TestMongoPingContract(t *testing.T) {
	connection := newMongoMockConnection(t, mongoMockSuccessResponse(), mongoMockCommandErrorResponse(91, "shutdown in progress"))
	if err := connection.PingContext(context.Background()); err != nil {
		t.Fatalf("健康节点 ping 失败: %v", err)
	}
	if err := connection.PingContext(context.Background()); err == nil {
		t.Fatal("节点拒绝 ping 时不应报告健康")
	}
	//lint:ignore SA1012 本例明确验证驱动拒绝空探针上下文。
	if err := connection.PingContext(nil); err == nil {
		t.Fatal("空探针上下文应失败")
	}
	if err := (*MongoConnection)(nil).PingContext(context.Background()); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("空连接不应报告健康: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := connection.PingContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消原因丢失: %v", err)
	}
}
