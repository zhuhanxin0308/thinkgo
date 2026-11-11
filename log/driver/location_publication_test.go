package driver

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

type delayedFileLocation struct {
	*File
	old     *time.Location
	entered chan struct{}
	release chan struct{}
	blocked atomic.Bool
}

func (driver *delayedFileLocation) SetLocation(location *time.Location) {
	if location == driver.old && driver.blocked.CompareAndSwap(false, true) {
		close(driver.entered)
		<-driver.release
	}
	driver.File.SetLocation(location)
}

// TestFileRetentionUsesLatestPublishedLocation 验证旧通知晚到后，真实文件清理仍按新时区的日期边界执行。
func TestFileRetentionUsesLatestPublishedLocation(t *testing.T) {
	const secondsPerHour = int(time.Hour / time.Second)
	old := time.FixedZone("旧时区", -12*secondsPerHour)
	latest := time.FixedZone("新时区", 14*secondsPerHour)
	directory := t.TempDir()
	file, err := NewFileWithOptions(directory, FileOptions{RetentionDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	logger := frameworklog.NewLog()
	logger.SetLocation(old)
	driver := &delayedFileLocation{File: file, old: old, entered: make(chan struct{}), release: make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- logger.AddDriver(driver) }()
	const completionTimeout = 3 * time.Second
	select {
	case <-driver.entered:
	case <-time.After(completionTimeout):
		t.Fatal("未进入旧文件驱动通知")
	}
	logger.SetLocation(latest)
	close(driver.release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(completionTimeout):
		t.Fatal("新增文件驱动未完成")
	}
	expired := filepath.Join(directory, "2026-11-09.log")
	if err := os.WriteFile(expired, []byte("历史日志\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	boundary := time.Date(2026, time.November, 11, 0, 30, 0, 0, latest)
	file.mu.Lock()
	actual := file.location
	err = file.cleanupLocked(boundary.In(actual))
	file.mu.Unlock()
	if err != nil || actual != latest {
		t.Fatalf("文件驱动清理时区错误: location=%v err=%v", actual, err)
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧时区导致应过期的日志被保留: %v", err)
	}
}
