package db

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestManagerLazilyBuildsAndReusesConnection 验证配置连接在首次访问前不会创建，
// 并发和重复访问都复用同一个已安装连接。
func TestManagerLazilyBuildsAndReusesConnection(t *testing.T) {
	manager := NewManager("primary")
	t.Cleanup(func() { _ = manager.Close() })
	var calls atomic.Int32
	release := make(chan struct{})
	if err := manager.RegisterFactory("primary", func() (*DB, error) {
		calls.Add(1)
		<-release
		return NewDB(&fakeManagerConnection{name: "primary"}), nil
	}); err != nil {
		t.Fatalf("注册惰性连接工厂失败: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("注册连接工厂不应建立连接，实际调用 %d 次", calls.Load())
	}

	const readers = 16
	connections := make(chan *DB, readers)
	errorsFound := make(chan error, readers)
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := 0; index < readers; index++ {
		go func() {
			defer wait.Done()
			connection, err := manager.Default()
			connections <- connection
			errorsFound <- err
		}()
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for calls.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("首次访问未触发连接工厂")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("并发首次访问只能执行一个连接工厂，实际为 %d", calls.Load())
	}
	close(release)
	wait.Wait()
	close(connections)
	close(errorsFound)

	var first *DB
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("解析惰性连接失败: %v", err)
		}
	}
	for connection := range connections {
		if connection == nil {
			t.Fatal("惰性连接不能为空")
		}
		if first == nil {
			first = connection
		} else if connection != first {
			t.Fatal("并发访问必须复用同一个连接实例")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("连接安装后不得重复执行工厂，实际为 %d", calls.Load())
	}
}

// TestManagerLazyFactoryFailureCanRetry 验证一次环境连接故障不会永久污染管理器，
// 后续访问可在数据库恢复后重新建立连接。
func TestManagerLazyFactoryFailureCanRetry(t *testing.T) {
	manager := NewManager("primary")
	t.Cleanup(func() { _ = manager.Close() })
	transient := errors.New("temporary database failure")
	var calls atomic.Int32
	if err := manager.RegisterFactory("primary", func() (*DB, error) {
		if calls.Add(1) == 1 {
			return nil, transient
		}
		return NewDB(&fakeManagerConnection{name: "primary"}), nil
	}); err != nil {
		t.Fatalf("注册惰性连接工厂失败: %v", err)
	}
	if connection, err := manager.Default(); connection != nil || !errors.Is(err, transient) {
		t.Fatalf("首次连接故障应原样返回: connection=%#v err=%v", connection, err)
	}
	connection, err := manager.Default()
	if err != nil || connection == nil {
		t.Fatalf("第二次连接应允许恢复: connection=%#v err=%v", connection, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("恢复流程应执行两次连接工厂，实际为 %d", calls.Load())
	}
}

// TestManagerCloseDoesNotBuildUnusedFactory 验证应用关闭时不会为了释放资源反向建立
// 从未使用过的数据库连接。
func TestManagerCloseDoesNotBuildUnusedFactory(t *testing.T) {
	manager := NewManager("primary")
	var calls atomic.Int32
	if err := manager.RegisterFactory("primary", func() (*DB, error) {
		calls.Add(1)
		return NewDB(&fakeManagerConnection{name: "primary"}), nil
	}); err != nil {
		t.Fatalf("注册惰性连接工厂失败: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("关闭惰性管理器失败: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("关闭未使用管理器不应建立连接，实际调用 %d 次", calls.Load())
	}
	if connection, err := manager.Default(); connection != nil || !errors.Is(err, ErrDatabaseManagerClosed) {
		t.Fatalf("关闭后访问应返回管理器关闭错误: connection=%#v err=%v", connection, err)
	}
}
