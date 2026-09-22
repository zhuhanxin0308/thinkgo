package db

import (
	"context"
	"errors"
	"testing"
)

// TestDatabasePingLifecycle 验证探针保留取消、能力缺失和关闭错误，绝不伪报健康。
func TestDatabasePingLifecycle(t *testing.T) {
	connection, _ := newStatementCacheConnection(t)
	database := NewDB(connection)
	if err := database.PingContext(context.Background()); err != nil {
		t.Fatalf("原生连接池探测失败: %v", err)
	}
	//lint:ignore SA1012 本例明确验证公共探针拒绝空上下文。
	if err := database.PingContext(nil); !errors.Is(err, ErrInvalidDatabaseContext) {
		t.Fatalf("空上下文错误丢失: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := database.PingContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("探针取消原因丢失: %v", err)
	}
	if err := NewDB(&fakeManagerConnection{}).PingContext(context.Background()); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("没有探针能力的驱动不应报告健康: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.PingContext(context.Background()); !errors.Is(err, ErrDatabaseClosed) {
		t.Fatalf("关闭后的数据库不应报告健康: %v", err)
	}
	if err := (*DB)(nil).PingContext(context.Background()); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("空数据库不应报告健康: %v", err)
	}
	if err := (*SQLConnection)(nil).PingContext(context.Background()); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("空原生连接不应报告健康: %v", err)
	}
	//lint:ignore SA1012 本例明确验证原生探针拒绝空上下文。
	if err := connection.PingContext(nil); !errors.Is(err, ErrInvalidDatabaseContext) {
		t.Fatalf("原生探针应拒绝空上下文: %v", err)
	}
}

// TestManagerLoadedDefaultDoesNotConnect 验证状态观察不会意外建立惰性连接。
func TestManagerLoadedDefaultDoesNotConnect(t *testing.T) {
	manager := NewManager("primary")
	t.Cleanup(func() { _ = manager.Close() })
	called := false
	connection, _ := newStatementCacheConnection(t)
	database := NewDB(connection)
	if err := manager.RegisterFactory("primary", func() (*DB, error) {
		called = true
		return database, nil
	}); err != nil {
		t.Fatal(err)
	}
	if current, loaded, err := manager.DefaultIfLoaded(); err != nil || loaded || current != nil || called {
		t.Fatalf("观察惰性状态触发了连接: loaded=%t called=%t err=%v", loaded, called, err)
	}
	if _, err := manager.Default(); err != nil {
		t.Fatal(err)
	}
	if current, loaded, err := manager.DefaultIfLoaded(); err != nil || !loaded || current != database {
		t.Fatalf("已加载连接未被观察到: loaded=%t err=%v", loaded, err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.DefaultIfLoaded(); !errors.Is(err, ErrDatabaseManagerClosed) {
		t.Fatalf("关闭的管理器错误丢失: %v", err)
	}
	if _, _, err := (*Manager)(nil).DefaultIfLoaded(); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("空管理器错误丢失: %v", err)
	}
	if _, _, err := NewManager("invalid name").DefaultIfLoaded(); !errors.Is(err, ErrInvalidConnectionName) {
		t.Fatalf("非法默认连接名错误丢失: %v", err)
	}
}
