package filesystem

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

type reentrantCloseDriver struct {
	Driver
	manager *Filesystem
	closed  atomic.Int32
}

func (driver *reentrantCloseDriver) Close() error {
	_ = driver.manager.GetDefaultDriver()
	_ = driver.manager.GetConfig()
	driver.closed.Add(1)
	return nil
}

// TestDiscardedDiskCloseReentry 用阶段屏障固定交错，独立进程保证旧实现死锁不会挂住测试套件。
func TestDiscardedDiskCloseReentry(t *testing.T) {
	const childFlag = "THINKGO_DISK_REENTRY_CHILD"
	if os.Getenv(childFlag) != "1" {
		const timeout = 5 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiscardedDiskCloseReentry$")
		command.Env = append(os.Environ(), childFlag+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("丢弃驱动时关闭回调重入失败: %v, 超时=%v, 输出=%s", err, ctx.Err(), output)
		}
		return
	}
	for _, closeBeforePublish := range []bool{false, true} {
		manager := New(map[string]interface{}{
			"default": "custom",
			"disks":   map[string]interface{}{"custom": map[string]interface{}{"type": "callback"}},
		})
		const competingCalls = 2
		entered := make(chan *reentrantCloseDriver, competingCalls)
		release := make(chan struct{})
		if err := manager.Extend("callback", func(map[string]interface{}) (Driver, error) {
			driver := &reentrantCloseDriver{manager: manager}
			entered <- driver
			<-release
			return driver, nil
		}); err != nil {
			t.Fatal(err)
		}
		type result struct {
			driver Driver
			err    error
		}
		results := make(chan result, competingCalls)
		for range competingCalls {
			go func() {
				driver, err := manager.Disk("custom")
				results <- result{driver: driver, err: err}
			}()
		}
		created := []*reentrantCloseDriver{<-entered, <-entered}
		if closeBeforePublish {
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
		}
		close(release)
		first, second := <-results, <-results
		if closeBeforePublish {
			if first.driver != nil || second.driver != nil || !errors.Is(first.err, ErrFilesystemClosed) || !errors.Is(second.err, ErrFilesystemClosed) {
				t.Fatal("关闭后晚到的驱动不应发布")
			}
		} else {
			if first.err != nil || second.err != nil || first.driver != second.driver {
				t.Fatalf("竞争调用未复用同一实例: %v, %v", first.err, second.err)
			}
			if got := created[0].closed.Load() + created[1].closed.Load(); got != 1 {
				t.Fatalf("发布前应仅关闭多余实例一次，实际=%d", got)
			}
			manager.ForgetDriver("custom")
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		for _, driver := range created {
			if driver.closed.Load() != 1 {
				t.Fatalf("驱动未被恰好关闭一次: %d", driver.closed.Load())
			}
		}
	}
}
