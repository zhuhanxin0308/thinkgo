//go:build linux || darwin

package driver

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestGoTemplateRejectsFIFOBeforeWaitingForWriter(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe.html")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": root, "cache": true}); err != nil {
		t.Fatal(err)
	}
	const calls = 3
	results := make(chan error, calls)
	for range 2 {
		go func() {
			_, err := driver.Fetch("pipe", nil)
			results <- err
		}()
	}
	go func() {
		_, err := driver.Exists("pipe")
		results <- err
	}()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for remaining := calls; remaining > 0; remaining-- {
		select {
		case err := <-results:
			if !errors.Is(err, ErrTemplateNotRegular) {
				t.Errorf("模板 FIFO 应直接拒绝: %v", err)
			}
		case <-timeout.C:
			// 给旧的阻塞打开提供写端，避免失败的回归测试遗留等待协程。
			fd, err := unix.Open(fifo, unix.O_RDWR|unix.O_NONBLOCK, 0)
			if err != nil {
				t.Fatalf("释放模板 FIFO 失败: %v", err)
			}
			defer unix.Close(fd)
			for range remaining {
				select {
				case <-results:
				case <-time.After(time.Second):
					t.Fatal("模板等待协程未释放")
				}
			}
			t.Fatal("模板探测在没有 FIFO 写端时阻塞")
		}
	}
}
