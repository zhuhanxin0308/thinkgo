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

// TestManagerUnattachedAliasCleanupPreservesSharedActiveConnection 验证惰性工厂返回包装共享物理连接的 *DB
// 在挂载失败或并发关闭时不会绕过主连接租约直接关闭共享物理连接 (#599)。
func TestManagerUnattachedAliasCleanupPreservesSharedActiveConnection(t *testing.T) {
	t.Run("并发关闭期间未挂载别名不得提前切断活跃共享连接", func(t *testing.T) {
		shared := &blockingLifecycleConnection{
			started: make(chan struct{}),
			release: make(chan struct{}),
			closed:  make(chan struct{}),
		}
		primary := NewDB(shared)
		manager := NewManager("primary")
		if err := manager.Add("primary", primary); err != nil {
			t.Fatalf("add primary: %v", err)
		}

		factoryEntered := make(chan struct{})
		allowFactoryReturn := make(chan struct{})
		if err := manager.RegisterFactory("alias", func() (*DB, error) {
			close(factoryEntered)
			<-allowFactoryReturn
			return NewDB(shared), nil
		}); err != nil {
			t.Fatalf("register alias factory: %v", err)
		}

		queryDone := make(chan error, 1)
		go func() {
			_, err := primary.Table("users").Select()
			queryDone <- err
		}()
		<-shared.started

		aliasDone := make(chan error, 1)
		go func() {
			_, err := manager.Connection("alias")
			aliasDone <- err
		}()
		<-factoryEntered

		closeDone := make(chan error, 1)
		go func() {
			closeDone <- manager.Close()
		}()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			manager.lock.RLock()
			closed := manager.closed
			manager.lock.RUnlock()
			if closed {
				break
			}
			time.Sleep(time.Millisecond)
		}

		close(allowFactoryReturn)
		aliasErr := <-aliasDone
		if !errors.Is(aliasErr, ErrDatabaseManagerClosed) {
			t.Fatalf("关闭期间惰性连接应返回 ErrDatabaseManagerClosed，实际为 %v", aliasErr)
		}

		select {
		case <-shared.closed:
			t.Fatal("primary 仍持有活跃查询租约时，alias 异常清理不得关闭共享物理连接")
		case <-time.After(30 * time.Millisecond):
		}

		close(shared.release)
		if err := <-queryDone; err != nil {
			t.Fatalf("primary 查询应正常完成: %v", err)
		}
		if err := <-closeDone; err != nil {
			t.Fatalf("manager close: %v", err)
		}
	})

	t.Run("挂载重名冲突时不得关闭已托管共享物理连接", func(t *testing.T) {
		shared := &identityCountingConnection{id: NewConnectionID("shared")}
		manager := NewManager("primary")
		t.Cleanup(func() { _ = manager.Close() })
		if err := manager.Add("primary", NewDB(shared)); err != nil {
			t.Fatalf("add primary: %v", err)
		}
		if err := manager.RegisterFactory("alias", func() (*DB, error) {
			manager.lock.Lock()
			manager.connections["alias"] = NewDB(&fakeManagerConnection{name: "winner"})
			manager.lock.Unlock()
			return NewDB(shared), nil
		}); err != nil {
			t.Fatalf("register alias factory: %v", err)
		}
		if _, err := manager.Connection("alias"); !errors.Is(err, ErrDuplicateConnection) {
			t.Fatalf("期望重名错误 ErrDuplicateConnection，实际为 %v", err)
		}
		if got := shared.closeCalls.Load(); got != 0 {
			t.Fatalf("primary 仍在运行时共享连接不得被关闭，closeCalls=%d", got)
		}
	})
}
