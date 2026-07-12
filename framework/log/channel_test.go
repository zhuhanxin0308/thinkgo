package log

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLogChannelsAreIsolated(t *testing.T) {
	defaultDriver := newMockDriver()
	sqlDriver := newMockDriver()

	logger := NewLog(defaultDriver)
	sqlChannel := NewLog(sqlDriver)
	sqlChannel.SetFlushInterval(50 * time.Millisecond)
	if err := logger.RegisterChannel("sql", sqlChannel); err != nil {
		t.Fatalf("注册 SQL 日志通道失败: %v", err)
	}
	logger.SetFlushInterval(50 * time.Millisecond)

	logger.Info("default")
	logger.Channel("sql").Info("sql")

	time.Sleep(100 * time.Millisecond)
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志通道失败: %v", err)
	}

	if defaultDriver.totalSavedCount() != 1 {
		t.Fatalf("默认通道应仅收到 1 条日志，实际为 %d", defaultDriver.totalSavedCount())
	}
	if sqlDriver.totalSavedCount() != 1 {
		t.Fatalf("sql 通道应仅收到 1 条日志，实际为 %d", sqlDriver.totalSavedCount())
	}

	defaultEntries := defaultDriver.allEntries()
	if len(defaultEntries) != 1 || defaultEntries[0].Message != "default" {
		t.Fatalf("默认通道日志内容不正确，实际为 %#v", defaultEntries)
	}
	sqlEntries := sqlDriver.allEntries()
	if len(sqlEntries) != 1 || sqlEntries[0].Message != "sql" {
		t.Fatalf("sql 通道日志内容不正确，实际为 %#v", sqlEntries)
	}
}

// TestRegisterChannelRejectsInvalidAndDuplicateGraph 验证通道注册不会覆盖旧值或形成关闭死锁环。
func TestRegisterChannelRejectsInvalidAndDuplicateGraph(t *testing.T) {
	root := NewLog(newMockDriver())
	child := NewLog(newMockDriver())

	if err := root.RegisterChannel("", child); !errors.Is(err, ErrInvalidLogChannel) {
		t.Fatalf("空通道名应返回 ErrInvalidLogChannel，实际为 %v", err)
	}
	if err := root.RegisterChannel("nil", nil); !errors.Is(err, ErrInvalidLogChannel) {
		t.Fatalf("nil 通道应返回 ErrInvalidLogChannel，实际为 %v", err)
	}
	if err := root.RegisterChannel("self", root); !errors.Is(err, ErrLogChannelCycle) {
		t.Fatalf("自引用通道应返回 ErrLogChannelCycle，实际为 %v", err)
	}
	if err := root.RegisterChannel("child", child); err != nil {
		t.Fatalf("首次注册子通道失败: %v", err)
	}
	if err := root.RegisterChannel("child", NewLog()); !errors.Is(err, ErrLogChannelExists) {
		t.Fatalf("重复通道名应返回 ErrLogChannelExists，实际为 %v", err)
	}
	if err := child.RegisterChannel("root", root); !errors.Is(err, ErrLogChannelCycle) {
		t.Fatalf("反向注册形成的环应返回 ErrLogChannelCycle，实际为 %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- root.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("关闭无环通道图失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭通道图发生死锁")
	}
}

// TestRegisterChannelRejectsClosedLogger 验证关闭后的父通道和子通道都不能重新加入图。
func TestRegisterChannelRejectsClosedLogger(t *testing.T) {
	closedParent := NewLog()
	if err := closedParent.Close(); err != nil {
		t.Fatalf("关闭父日志器失败: %v", err)
	}
	if err := closedParent.RegisterChannel("child", NewLog()); !errors.Is(err, ErrLogClosed) {
		t.Fatalf("关闭后的父日志器应拒绝注册，实际为 %v", err)
	}

	parent := NewLog()
	closedChild := NewLog()
	if err := closedChild.Close(); err != nil {
		t.Fatalf("关闭子日志器失败: %v", err)
	}
	if err := parent.RegisterChannel("closed", closedChild); !errors.Is(err, ErrLogClosed) {
		t.Fatalf("关闭后的子日志器应拒绝注册，实际为 %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("关闭父日志器失败: %v", err)
	}
}

// TestRegisterChannelConcurrentOppositeEdgesStayAcyclic 验证并发反向注册最多成功一条边。
func TestRegisterChannelConcurrentOppositeEdgesStayAcyclic(t *testing.T) {
	left := NewLog()
	right := NewLog()
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		<-start
		errorsFound <- left.RegisterChannel("right", right)
	}()
	go func() {
		defer waitGroup.Done()
		<-start
		errorsFound <- right.RegisterChannel("left", left)
	}()
	close(start)
	waitGroup.Wait()
	close(errorsFound)

	successes := 0
	cycles := 0
	for err := range errorsFound {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrLogChannelCycle):
			cycles++
		default:
			t.Fatalf("并发注册返回了意外错误: %v", err)
		}
	}
	if successes != 1 || cycles != 1 {
		t.Fatalf("并发反向注册应一条成功、一条拒绝，实际成功=%d 循环错误=%d", successes, cycles)
	}
	if err := left.Close(); err != nil {
		t.Fatalf("关闭左日志器失败: %v", err)
	}
	if err := right.Close(); err != nil {
		t.Fatalf("关闭右日志器失败: %v", err)
	}
}
