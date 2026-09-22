package db

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type identityCountingConnection struct {
	mockConnection
	id         ConnectionID
	closeCalls atomic.Int64
}

func (connection *identityCountingConnection) ConnectionID() ConnectionID { return connection.id }
func (connection *identityCountingConnection) Close() error {
	connection.closeCalls.Add(1)
	return nil
}

func TestManagerClosesSharedConnectionIdentityOnce(t *testing.T) {
	connection := &identityCountingConnection{id: NewConnectionID("shared")}
	manager := NewManager("first")
	if err := manager.Add("first", NewDB(connection)); err != nil {
		t.Fatalf("add first wrapper: %v", err)
	}
	if err := manager.Add("second", NewDB(connection)); err != nil {
		t.Fatalf("add second wrapper: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	if got := connection.closeCalls.Load(); got != 1 {
		t.Fatalf("shared connection closed %d times, want 1", got)
	}
}

// TestManagerCloseWaitsForSharedWrapperLease 验证共享物理连接在任一 DB 包装器仍有查询时不会被提前关闭。
func TestManagerCloseWaitsForSharedWrapperLease(t *testing.T) {
	connection := &blockingLifecycleConnection{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	first := NewDB(connection)
	second := NewDB(connection)
	manager := NewManager("first")
	if err := manager.Add("first", first); err != nil {
		t.Fatalf("add first wrapper: %v", err)
	}
	if err := manager.Add("second", second); err != nil {
		t.Fatalf("add second wrapper: %v", err)
	}

	queryDone := make(chan error, 1)
	go func() {
		_, err := second.Table("users").Select()
		queryDone <- err
	}()
	<-connection.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case <-connection.closed:
		t.Fatal("共享包装器仍有查询时 Manager 不得关闭物理连接")
	case <-time.After(50 * time.Millisecond):
	}

	close(connection.release)
	if err := <-queryDone; err != nil {
		t.Fatalf("共享包装器查询失败: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("manager close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("共享连接租约释放后 Manager 未完成关闭")
	}
}

type fakeManagerConnection struct {
	connectionIdentityState
	name string
}

func (c *fakeManagerConnection) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *fakeManagerConnection) Insert(context.Context, InsertRequest) (InsertResult, error) {
	return InsertResult{}, nil
}

func (c *fakeManagerConnection) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{}, nil
}

func (c *fakeManagerConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{}, nil
}

func (c *fakeManagerConnection) Count(context.Context, CountRequest) (int64, error) {
	return 0, nil
}

func (c *fakeManagerConnection) Close() error {
	return nil
}

func TestManagerReturnsNamedConnections(t *testing.T) {
	manager := NewManager("primary")
	primary := NewDB(&fakeManagerConnection{name: "primary"})
	analytics := NewDB(&fakeManagerConnection{name: "analytics"})

	if err := manager.Add("primary", primary); err != nil {
		t.Fatalf("注册默认连接失败，错误为 %v", err)
	}
	if err := manager.Add("analytics", analytics); err != nil {
		t.Fatalf("注册命名连接失败，错误为 %v", err)
	}

	defaultConnection, err := manager.Default()
	if err != nil {
		t.Fatalf("读取默认连接失败，错误为 %v", err)
	}
	if defaultConnection != primary {
		t.Fatal("默认连接返回不正确")
	}
	conn, err := manager.Connection("analytics")
	if err != nil {
		t.Fatalf("命名连接读取失败，错误为 %v", err)
	}
	if conn != analytics {
		t.Fatal("命名连接实例不正确")
	}
}
