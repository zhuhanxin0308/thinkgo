package driver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileSessionPausedWriterKeepsExclusiveOwnership 模拟持有者暂停超过旧租期，恢复后仍必须先于新持有者提交。
func TestFileSessionPausedWriterKeepsExclusiveOwnership(t *testing.T) {
	first, directory := newTestFileDriver(t)
	second, err := NewFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	const id = "paused-writer"
	if err := first.Write(id, "initial"); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Update(id, func(string, bool) (string, bool, error) {
			close(started)
			<-release
			return "first", false, nil
		})
	}()
	<-started
	// 只推进旧协议的磁盘到期记录，不等待真实三十秒；稳定 OS 锁不能依赖该载荷。
	legacyLock := filepath.Join(directory, hashedSessionFilename(id, ".lock"))
	if err := os.WriteFile(legacyLock, []byte(`{"owner":"expired-owner","expire_at":1}`), 0o600); err != nil {
		close(release)
		<-firstDone
		t.Fatal(err)
	}
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.Update(id, func(current string, found bool) (string, bool, error) {
			close(secondStarted)
			if !found || current != "first" {
				return "observed-stale", false, nil
			}
			return "second", false, nil
		})
	}()
	enteredBeforeRelease := false
	select {
	case <-secondStarted:
		enteredBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	firstErr, secondErr := <-firstDone, <-secondDone
	if enteredBeforeRelease || firstErr != nil || secondErr != nil {
		t.Fatalf("过期载荷破坏了稳定互斥: entered=%t first=%v second=%v", enteredBeforeRelease, firstErr, secondErr)
	}
	if value, found, err := first.Read(id); err != nil || !found || value != "second" {
		t.Fatalf("旧持有者覆盖新结果: value=%q found=%t err=%v", value, found, err)
	}
}
