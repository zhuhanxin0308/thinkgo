//go:build (linux && !android) || (darwin && !ios)

package driver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
	"golang.org/x/sys/unix"
)

func TestActiveLogDoesNotLockDataOrDirectory(t *testing.T) {
	directory := t.TempDir()
	driver := NewFile(directory)
	t.Cleanup(func() { _ = driver.Close() })
	now := time.Now().UTC()
	if err := (sharedLogWrite{Time: now, Message: "共享卷日志"}).appendTo(driver); err != nil {
		t.Fatal(err)
	}
	// CIFS 可强制实施数据锁。普通读写者应能独立使用日志；框架锁仅占用协调文件。
	data, err := os.OpenFile(filepath.Join(directory, now.Format("2006-01-02")+".log"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	dir, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	err = withLogDirectoryLock(directory, nil, func() error {
		for _, candidate := range []*os.File{data, dir} {
			if err := winfile.Flock(candidate, unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Errorf("协调协议占用了日志或目录的内核锁: %s: %v", candidate.Name(), err)
			} else if err := winfile.Flock(candidate, unix.LOCK_UN); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
