package winfile

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestGuardRetainsIdentityAndHonorsCancellation 验证等待可取消、错误回调释放内核锁且文件身份不轮换。
func TestGuardRetainsIdentityAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mutation.guard")
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	abort := errors.New("临界区失败")
	go func() {
		done <- WithGuard(context.Background(), path, func() error {
			close(started)
			<-release
			return abort
		})
	}()
	<-started
	before, err := os.Stat(path)
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	called := false
	err = WithGuard(ctx, path, func() error { called = true; return nil })
	close(release)
	ownerErr := <-done
	if called || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(ownerErr, abort) {
		t.Fatalf("锁取消或回调错误丢失: %t %v %v", called, err, ownerErr)
	}
	var nilContext context.Context
	if err := WithGuard(nilContext, path, func() error { return nil }); err != nil {
		t.Fatalf("前一回调失败后锁未释放: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("锁文件身份发生变化: %v", err)
	}
}

// TestGuardRejectsUnsafePaths 验证不安全路径和调用错误均在进入临界区前失败。
func TestGuardRejectsUnsafePaths(t *testing.T) {
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WithGuard(ctx, filepath.Join(directory, "unused"), func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消上下文被忽略: %v", err)
	}
	if err := WithGuard(context.Background(), directory, nil); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("空回调未被拒绝: %v", err)
	}
	for _, path := range []string{directory, filepath.Join(directory, "missing", "guard"), "bad\x00path"} {
		called := false
		if err := WithGuard(context.Background(), path, func() error { called = true; return nil }); err == nil || called {
			t.Fatalf("无效路径被接受: %q %t %v", path, called, err)
		}
	}
	target, link := filepath.Join(directory, "target"), filepath.Join(directory, "link")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("当前环境不能创建符号链接: %v", err)
	}
	if err := WithGuard(context.Background(), link, func() error { return nil }); !errors.Is(err, ErrUnsafeGuard) {
		t.Fatalf("符号链接锁路径被接受: %v", err)
	}
}

// TestGuardRecoversAfterProcessExit 验证进程被终止时内核释放锁，无需等待磁盘租约到期。
func TestGuardRecoversAfterProcessExit(t *testing.T) {
	const guardEnvironment = "THINKGO_GUARD_CRASH_TEST_PATH"
	if path := os.Getenv(guardEnvironment); path != "" {
		if err := WithGuard(context.Background(), path, func() error {
			fmt.Fprintln(os.Stdout, "guard-acquired")
			<-time.After(time.Minute)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "crash.guard")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGuardRecoversAfterProcessExit$")
	command.Env = append(os.Environ(), guardEnvironment+"="+path)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	line, readErr := bufio.NewReader(output).ReadString('\n')
	if readErr != nil || line != "guard-acquired\n" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("子进程未取得锁: %q %v", line, readErr)
	}
	if err := command.Process.Kill(); err != nil {
		_ = command.Wait()
		t.Fatal(err)
	}
	_ = command.Wait()
	if err := WithGuard(ctx, path, func() error { return nil }); err != nil {
		t.Fatalf("子进程退出后锁仍占用: %v", err)
	}
}
