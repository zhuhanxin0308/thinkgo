package db

import (
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// 工厂退出后，同一次构建的等待者必须收到失败，下一次访问可以重新建立连接。
func TestManagerFactoryGoexitPublishesFailureAndAllowsRetry(t *testing.T) {
	testManagerFactoryExit(t, func() (*DB, error) {
		runtime.Goexit()
		return nil, nil
	})
}

type exitingFactoryError struct{}

func (exitingFactoryError) Error() string {
	runtime.Goexit()
	return ""
}

// 生成错误文本也可能运行扩展代码，不能让它跳过结果发布。
func TestManagerFactoryErrorFormattingExitPublishesFailure(t *testing.T) {
	testManagerFactoryExit(t, func() (*DB, error) { return nil, exitingFactoryError{} })
}

func testManagerFactoryExit(t *testing.T, exit func() (*DB, error)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		manager := NewManager("primary")
		release := make(chan struct{})
		ownerExited := make(chan struct{})
		ownerReturned := make(chan struct{}, 1)
		var calls atomic.Int32
		backend := &identityCountingConnection{id: NewConnectionID("retry")}
		if err := manager.RegisterFactory("primary", func() (*DB, error) {
			if calls.Add(1) == 1 {
				<-release
				return exit()
			}
			return NewDB(backend), nil
		}); err != nil {
			t.Fatal(err)
		}
		go func() {
			defer close(ownerExited)
			_, _ = manager.Default()
			ownerReturned <- struct{}{}
		}()
		synctest.Wait()
		waiter := make(chan error, 1)
		go func() {
			connection, err := manager.Default()
			if connection != nil {
				t.Error("异常退出的构建发布了连接")
			}
			waiter <- err
		}()
		synctest.Wait()
		close(release)
		<-ownerExited
		synctest.Wait()
		select {
		case <-ownerReturned:
			t.Fatal("工厂的 Goexit 被吞掉")
		default:
		}
		select {
		case err := <-waiter:
			if !errors.Is(err, ErrConnectionFactoryAborted) {
				t.Fatalf("工厂退出没有可判别错误：%v", err)
			}
		default:
			t.Fatal("工厂已经退出，同次构建等待者仍未被唤醒")
		}
		connection, err := manager.Default()
		if err != nil || connection == nil || calls.Load() != 2 {
			t.Fatalf("工厂失败后未能重试：连接=%v，错误=%v，次数=%d", connection, err, calls.Load())
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		if backend.closeCalls.Load() != 1 {
			t.Fatalf("重试成功的物理连接关闭了 %d 次", backend.closeCalls.Load())
		}
	})
}

// Close 仍须等待正在运行的工厂；工厂随后 Goexit 也必须归还这次构建的计数。
func TestManagerCloseDrainsFactoryGoexit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewManager("primary")
		release := make(chan struct{})
		ownerExited := make(chan struct{})
		if err := manager.RegisterFactory("primary", func() (*DB, error) {
			<-release
			runtime.Goexit()
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
		go func() {
			defer close(ownerExited)
			_, _ = manager.Default()
		}()
		synctest.Wait()
		closed := make(chan error, 1)
		go func() { closed <- manager.Close() }()
		synctest.Wait()
		select {
		case err := <-closed:
			t.Fatalf("工厂尚未退出，关闭已返回：%v", err)
		default:
		}
		close(release)
		<-ownerExited
		synctest.Wait()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("工厂已退出，关闭仍在等待遗留计数")
		}
	})
}

// 兼容模式的 nil panic 与普通 panic 都是可重试错误，不能被当成成功或协程主动退出。
func TestManagerFactoryPanicsRemainRetryable(t *testing.T) {
	t.Setenv("GODEBUG", "panicnil=1")
	for _, value := range []any{nil, "连接工厂异常"} {
		manager := NewManager("primary")
		var calls int
		if err := manager.RegisterFactory("primary", func() (*DB, error) {
			calls++
			if calls == 1 {
				panic(value)
			}
			return NewDB(&fakeManagerConnection{name: "retry"}), nil
		}); err != nil {
			t.Fatal(err)
		}
		connection, err := manager.Default()
		if connection != nil || err == nil || errors.Is(err, ErrConnectionFactoryAborted) || !strings.Contains(err.Error(), "panic:") {
			t.Fatalf("工厂 panic 分类错误：连接=%v，错误=%v", connection, err)
		}
		if connection, err = manager.Default(); connection == nil || err != nil || calls != 2 {
			t.Fatalf("工厂 panic 后重试失败：连接=%v，错误=%v，次数=%d", connection, err, calls)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
