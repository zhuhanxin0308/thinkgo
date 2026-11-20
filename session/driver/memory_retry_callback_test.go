package driver

import (
	"errors"
	"sync"
	"testing"
	"time"
)

const memoryRetryTestTimeout = 2 * time.Second

// TestMemoryRetryCallbackCanReadAnotherSession 验证淘汰后的回调仍能重入公开只读接口。
func TestMemoryRetryCallbackCanReadAnotherSession(t *testing.T) {
	memory, err := NewMemoryWithMaxEntries(1)
	if err != nil {
		t.Fatal(err)
	}
	ids := distinctShardSessionIDs(t, memory, 2)
	if err := memory.Write(ids[0], "old"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseCallback := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseCallback)
	readDone := make(chan error, 1)
	updateDone := make(chan error, 1)
	calls := 0
	go func() {
		updateDone <- memory.Update(ids[0], func(_ string, found bool) (string, bool, error) {
			calls++
			if calls == 1 {
				close(entered)
				<-release
				return "stale", false, nil
			}
			if found {
				return "", false, errors.New("重试未观察到淘汰")
			}
			go func() {
				value, exists, readErr := memory.Read(ids[1])
				if readErr == nil && (!exists || value != "new") {
					readErr = errors.New("回调未读到新会话")
				}
				readDone <- readErr
			}()
			select {
			case readErr := <-readDone:
				readDone <- readErr
				return "", true, readErr
			case <-time.After(memoryRetryTestTimeout):
				return "", false, errors.New("重试回调阻塞了其他会话读取")
			}
		})
	}()
	select {
	case <-entered:
	case err := <-updateDone:
		t.Fatalf("首次回调未执行，更新已结束: %v", err)
	case <-time.After(memoryRetryTestTimeout):
		t.Fatal("首次回调未在期限内开始")
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- memory.Write(ids[1], "new") }()
	if err := awaitMemoryRetryResult(t, writeDone, "写入新会话"); err != nil {
		t.Fatal(err)
	}
	releaseCallback()
	if err := awaitMemoryRetryResult(t, updateDone, "更新旧会话"); err != nil {
		t.Error(err)
	}
	// 失败版本释放全局锁后也等待读协程退出，避免回归测试遗留工作。
	if err := awaitMemoryRetryResult(t, readDone, "回调中的独立读取"); err != nil {
		t.Error(err)
	}
	if calls != 2 {
		t.Errorf("淘汰重试次数错误: %d", calls)
	}
	if _, found, err := memory.Read(ids[0]); err != nil || found {
		t.Errorf("淘汰的旧会话不得复活: found=%t err=%v", found, err)
	}
	if value, found, err := memory.Read(ids[1]); err != nil || !found || value != "new" {
		t.Errorf("新会话被错误修改: value=%q found=%t err=%v", value, found, err)
	}
}

// TestMemoryRetryCallbackDoesNotBlockUnrelatedReads 验证慢重试回调不会占有整店读锁。
func TestMemoryRetryCallbackDoesNotBlockUnrelatedReads(t *testing.T) {
	memory, err := NewMemoryWithMaxEntries(1)
	if err != nil {
		t.Fatal(err)
	}
	ids := distinctShardSessionIDs(t, memory, 2)
	if err := memory.Write(ids[0], "old"); err != nil {
		t.Fatal(err)
	}
	firstEntered, firstRelease := make(chan struct{}), make(chan struct{})
	retryEntered, retryRelease := make(chan struct{}), make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(firstRelease) })
	releaseRetry := sync.OnceFunc(func() { close(retryRelease) })
	t.Cleanup(releaseFirst)
	t.Cleanup(releaseRetry)
	updateDone := make(chan error, 1)
	go func() {
		calls := 0
		updateDone <- memory.Update(ids[0], func(string, bool) (string, bool, error) {
			calls++
			if calls == 1 {
				close(firstEntered)
				<-firstRelease
				return "stale", false, nil
			}
			close(retryEntered)
			<-retryRelease
			return "", true, nil
		})
	}()
	select {
	case <-firstEntered:
	case err := <-updateDone:
		t.Fatalf("首次回调未执行，更新已结束: %v", err)
	case <-time.After(memoryRetryTestTimeout):
		t.Fatal("首次回调未在期限内开始")
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- memory.Write(ids[1], "new") }()
	if err := awaitMemoryRetryResult(t, writeDone, "写入新会话"); err != nil {
		t.Fatal(err)
	}
	releaseFirst()
	select {
	case <-retryEntered:
	case err := <-updateDone:
		t.Fatalf("淘汰后未重试，更新已结束: %v", err)
	case <-time.After(memoryRetryTestTimeout):
		t.Fatal("淘汰后的回调未在期限内开始")
	}
	readDone := make(chan error, 1)
	go func() {
		if value, found, err := memory.Read(ids[1]); err != nil || !found || value != "new" {
			readDone <- errors.New("独立读取未返回预期的新会话")
			return
		}
		readDone <- nil
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(memoryRetryTestTimeout):
		t.Error("独立读取必须在重试回调返回前完成")
		releaseRetry()
		if err := awaitMemoryRetryResult(t, readDone, "释放重试后的独立读取"); err != nil {
			t.Error(err)
		}
	}
	releaseRetry()
	if err := awaitMemoryRetryResult(t, updateDone, "完成重试更新"); err != nil {
		t.Fatal(err)
	}
}

// awaitMemoryRetryResult 让回归为不重试或提前返回的实现也只能导致有界测试失败。
func awaitMemoryRetryResult(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(memoryRetryTestTimeout):
		t.Fatalf("%s 未在期限内完成", operation)
		return nil
	}
}
