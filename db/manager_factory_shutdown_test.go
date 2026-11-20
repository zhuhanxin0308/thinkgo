package db

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

// 关闭只唤醒等待同一结果的调用者，不能代替仍在运行的工厂交还资源。
func TestManagerCloseWakesWaitersAndDrainsFactory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewManager("primary")
		backendErr := errors.New("共享连接关闭失败")
		backend := &lifecycleConnection{closeErr: backendErr}
		loaded := NewDB(backend)
		if err := manager.Add("loaded", loaded); err != nil {
			t.Fatal(err)
		}
		lease := make(chan struct{})
		releaseLease := sync.OnceFunc(func() { close(lease) })
		defer releaseLease()
		leaseDone := make(chan error, 1)
		go func() {
			leaseDone <- loaded.WithConnection(func(Connection) error {
				<-lease
				return nil
			})
		}()
		release := make(chan struct{})
		releaseFactory := sync.OnceFunc(func() { close(release) })
		defer releaseFactory()
		factoryErr := errors.New("工厂自身的错误")
		if err := manager.RegisterFactory("primary", func() (*DB, error) {
			<-release
			return nil, factoryErr
		}); err != nil {
			t.Fatal(err)
		}
		owner := make(chan error, 1)
		go func() {
			_, err := manager.Default()
			owner <- err
		}()
		synctest.Wait()

		waiter := make(chan error, 1)
		go func() {
			_, err := manager.Default()
			waiter <- err
		}()
		synctest.Wait()
		const closers = 2
		closed := make(chan error, closers)
		go func() { closed <- manager.Close() }()
		synctest.Wait()

		select {
		case err := <-waiter:
			if !errors.Is(err, ErrDatabaseManagerClosed) {
				t.Fatalf("在途等待者应收到管理器关闭错误，实际为 %v", err)
			}
		default:
			t.Fatal("管理器关闭后，在途等待者仍被不合作的工厂阻塞")
		}
		if calls := backend.closedTimes(); calls != 0 {
			t.Fatalf("租约未释放，物理连接已关闭 %d 次", calls)
		}
		releaseLease()
		if err := <-leaseDone; err != nil {
			t.Fatalf("关闭期间已取得的租约应正常完成，实际为 %v", err)
		}
		synctest.Wait()
		select {
		case err := <-closed:
			t.Fatalf("工厂尚未退出，Close 已返回 %v", err)
		default:
		}

		go func() { closed <- manager.Close() }()
		releaseFactory()
		if err := <-owner; !errors.Is(err, factoryErr) {
			t.Fatalf("首次调用应保留工厂的真实错误，实际为 %v", err)
		}
		for range closers {
			if err := <-closed; !errors.Is(err, backendErr) {
				t.Fatalf("并发 Close 应返回共享连接的关闭错误，实际为 %v", err)
			}
		}
		if calls := backend.closedTimes(); calls != 1 {
			t.Fatalf("共享连接关闭了 %d 次", calls)
		}
	})
}

// 各工厂在同一屏障后开始解析依赖，分别覆盖自引用、嵌套启动和两个并发首访互等。
func TestManagerCloseDrainsRecursiveFactories(t *testing.T) {
	for _, test := range []struct {
		name         string
		roots        []string
		dependencies map[string]string
	}{
		{"自引用", []string{"a"}, map[string]string{"a": "a"}},
		{"嵌套工厂互等", []string{"a"}, map[string]string{"a": "b", "b": "a"}},
		{"并发工厂互等", []string{"a", "b"}, map[string]string{"a": "b", "b": "a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				manager := NewManager("a")
				begin := make(chan struct{})
				for name, dependency := range test.dependencies {
					if err := manager.RegisterFactory(name, func() (*DB, error) {
						<-begin
						return manager.Connection(dependency)
					}); err != nil {
						t.Fatal(err)
					}
				}
				results := make(chan error, len(test.roots))
				for _, name := range test.roots {
					go func() {
						_, err := manager.Connection(name)
						results <- err
					}()
				}
				synctest.Wait()
				close(begin)
				synctest.Wait()

				if err := manager.Close(); err != nil {
					t.Fatal(err)
				}
				for range test.roots {
					if err := <-results; !errors.Is(err, ErrDatabaseManagerClosed) {
						t.Fatalf("关闭期间等待工厂的调用应返回关闭错误，实际为 %v", err)
					}
				}
			})
		})
	}
}

// 已经发布的结果归属于本次构建，即使等待者恢复执行前管理器也已发出关闭通知。
func TestLazyConnectionAttemptWaitPrefersPublishedResult(t *testing.T) {
	closing := make(chan struct{})
	close(closing)
	for _, posted := range []struct {
		connection *DB
		err        error
	}{
		{connection: NewDB(&fakeManagerConnection{name: "ready"})},
		{err: errors.New("已发布的工厂错误")},
	} {
		attempt := &lazyConnectionAttempt{
			done:       make(chan struct{}),
			connection: posted.connection,
			err:        posted.err,
		}
		close(attempt.done)
		got, err := attempt.wait(closing)
		if got != posted.connection || err != posted.err {
			t.Fatalf("已发布结果被关闭通知覆盖：连接 %p，错误 %v", got, err)
		}
	}
}

func TestManagerCloseWithoutConstructor(t *testing.T) {
	var absent *Manager
	if err := absent.Close(); err != nil {
		t.Fatalf("空指针管理器关闭失败：%v", err)
	}
	var zero Manager
	if _, err := zero.Connection("primary"); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("零值管理器应报告连接不存在：%v", err)
	}
	if err := zero.Close(); err != nil {
		t.Fatalf("零值管理器关闭失败：%v", err)
	}
	if _, err := zero.Connection("primary"); !errors.Is(err, ErrDatabaseManagerClosed) {
		t.Fatalf("零值管理器关闭后应拒绝解析：%v", err)
	}
}
