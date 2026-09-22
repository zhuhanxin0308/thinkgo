package db

import (
	"context"
	"testing"
	"time"
)

type queryContextCaptureConnection struct {
	*mockConnection
	contexts chan context.Context
}

func (connection *queryContextCaptureConnection) Select(ctx context.Context, _ SelectRequest) ([]map[string]interface{}, error) {
	connection.contexts <- ctx
	return nil, nil
}

// TestQueryAppliesDefaultOperationTimeout 验证 ORM 在调用方未提供截止时间时，
// 会为每次真实数据库操作建立统一的有限执行窗口。
func TestQueryAppliesDefaultOperationTimeout(t *testing.T) {
	connection := &queryContextCaptureConnection{
		mockConnection: &mockConnection{},
		contexts:       make(chan context.Context, 1),
	}
	database := NewDB(connection)
	if _, err := database.Table("users").Select(); err != nil {
		t.Fatalf("执行默认上下文查询失败: %v", err)
	}
	operationContext := <-connection.contexts
	deadline, exists := operationContext.Deadline()
	if !exists {
		t.Fatal("ORM 默认查询必须携带截止时间")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > defaultDatabaseOperationTimeout {
		t.Fatalf("ORM 默认截止时间错误: %v", remaining)
	}
}

// TestQueryPreservesExplicitDeadline 验证调用方提供的更长或更短截止时间均保持原值，
// 框架只补齐缺失边界，不擅自延长或缩短显式请求生命周期。
func TestQueryPreservesExplicitDeadline(t *testing.T) {
	connection := &queryContextCaptureConnection{
		mockConnection: &mockConnection{},
		contexts:       make(chan context.Context, 1),
	}
	database := NewDB(connection)
	expectedDeadline := time.Now().Add(2 * defaultDatabaseOperationTimeout)
	explicit, cancel := context.WithDeadline(context.Background(), expectedDeadline)
	defer cancel()

	if _, err := database.Table("users").WithContext(explicit).Select(); err != nil {
		t.Fatalf("执行显式上下文查询失败: %v", err)
	}
	actualDeadline, exists := (<-connection.contexts).Deadline()
	if !exists || !actualDeadline.Equal(expectedDeadline) {
		t.Fatalf("显式截止时间被修改: want=%v got=%v exists=%v", expectedDeadline, actualDeadline, exists)
	}
}
