package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const (
	// Readiness is fixture setup, not the cancellation latency contract.
	fakeGoReadyTimeout  = 15 * time.Second
	fakeGoCancelTimeout = time.Second
	// The blocked tool must never exit successfully by itself.
	fakeGoWatchdog = 30 * time.Second
)

// TestMain 在子进程模式下模拟会阻塞的 Go 工具，不依赖宿主 shell。
func TestMain(m *testing.M) {
	if os.Getenv("THINKGO_TEST_FAKE_GO") == "1" {
		os.Exit(runFakeGo())
	}
	os.Exit(m.Run())
}

func runFakeGo() int {
	mode := os.Getenv("THINKGO_TEST_FAKE_GO_STAGE")
	if mode != "source" && len(os.Args) > 1 && os.Args[1] == "list" {
		for _, argument := range os.Args[2:] {
			if argument == "-find" {
				entry := map[string]interface{}{
					"Dir":  filepath.Join(os.Getenv("THINKGO_TEST_PROJECT_BASE"), "app", "index", "command"),
					"Name": "command", "GoFiles": []string{"probe.go"},
				}
				if err := json.NewEncoder(os.Stdout).Encode(entry); err != nil {
					return 2
				}
				return 0
			}
		}
	}
	if mode == "exports" && len(os.Args) > 1 && os.Args[1] == "env" {
		_, _ = fmt.Fprintln(os.Stdout, "amd64")
		return 0
	}
	if delayText := os.Getenv("THINKGO_TEST_GO_START_DELAY"); delayText != "" {
		delay, err := time.ParseDuration(delayText)
		if err != nil || delay < 0 || delay > 5*time.Second {
			return 2
		}
		time.Sleep(delay)
	}
	if err := os.WriteFile(os.Getenv("THINKGO_TEST_GO_STARTED"), []byte("started"), 0o600); err != nil {
		return 2
	}
	time.Sleep(fakeGoWatchdog)
	return 2
}

// TestProjectCommandDiscoveryHonorsCancellation 验证命令发现中的 Go 子进程服从调用方取消。
func TestProjectCommandDiscoveryHonorsCancellation(t *testing.T) {
	for _, stage := range []string{"source", "architecture", "exports"} {
		t.Run(stage, func(t *testing.T) {
			assertProjectCommandDiscoveryCancellation(t, stage)
		})
	}
}

func assertProjectCommandDiscoveryCancellation(t *testing.T, stage string) {
	t.Helper()
	base := t.TempDir()
	writeProjectCommandFixture(t, base, "go.mod", "module example.com/project\n\ngo 1.24\n")
	writeProjectCommandFixture(t, base, "app/index/command/probe.go", "package command\ntype Probe struct{}\n")
	bin := t.TempDir()
	started := filepath.Join(t.TempDir(), "go-started")
	toolName := "go"
	if runtime.GOOS == "windows" {
		toolName = "go.exe"
	}
	tool := filepath.Join(bin, toolName)
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(tool, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	// Retain race checks and caller options, but remove the helper's artificial exit wait.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	t.Setenv("THINKGO_TEST_FAKE_GO", "1")
	t.Setenv("THINKGO_TEST_FAKE_GO_STAGE", stage)
	t.Setenv("THINKGO_TEST_GO_STARTED", started)
	t.Setenv("THINKGO_TEST_PROJECT_BASE", base)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	var runErr error
	var stderr bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = Run(ctx, base, []string{"list"}, io.Discard, &stderr, nil)
	}()
	// Registered after Setenv/TempDir: join before restoring the environment or
	// deleting files, including on a readiness timeout or a failed assertion.
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("fake Go stage=%s result=%v stderr=%q", stage, runErr, stderr.String())
		}
	})
	deadline := time.NewTimer(fakeGoReadyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("读取 Go 工具就绪标记失败: %v", err)
		}
		select {
		case <-done:
			t.Fatalf("Go 工具尚未就绪，发现流程已结束: %v", runErr)
		case <-deadline.C:
			t.Fatalf("Go 工具测试夹具未在 %s 内就绪（阶段 %s），尚未执行取消断言", fakeGoReadyTimeout, stage)
		case <-tick.C:
		}
	}
	begin := time.Now()
	cancel()
	cancellation := time.NewTimer(fakeGoCancelTimeout)
	defer cancellation.Stop()
	select {
	case <-done:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("取消后未返回 context.Canceled: %v", runErr)
		}
		if elapsed := time.Since(begin); elapsed > fakeGoCancelTimeout {
			t.Fatalf("取消后仍等待 Go 工具退出: %v", elapsed)
		}
	case <-cancellation.C:
		t.Fatal("取消后命令发现仍被 Go 工具阻塞")
	}
}

// The fixture must not inherit artificial race-runtime exit waits from its caller.
func TestProjectCommandDiscoveryCancellationWithInheritedRaceOptions(t *testing.T) {
	const options = "halt_on_error=1 history_size=2 atexit_sleep_ms=1600"
	t.Setenv("GORACE", options)
	t.Run("exports", func(t *testing.T) {
		assertProjectCommandDiscoveryCancellation(t, "exports")
	})
	if got := os.Getenv("GORACE"); got != options {
		t.Fatalf("fixture leaked GORACE changes: got %q, want %q", got, options)
	}
}

// A controlled startup delay must not be confused with failure to honor cancellation.
func TestProjectCommandDiscoveryCancellationAfterSlowStart(t *testing.T) {
	const startupDelay = 3200 * time.Millisecond
	t.Setenv("THINKGO_TEST_GO_START_DELAY", startupDelay.String())
	begin := time.Now()
	assertProjectCommandDiscoveryCancellation(t, "source")
	if time.Since(begin) < startupDelay {
		t.Fatal("slow-start fixture did not exercise the configured delay")
	}
}
