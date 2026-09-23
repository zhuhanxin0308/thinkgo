package launcher

import (
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

const fakeGoWait = 3 * time.Second

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
	if err := os.WriteFile(os.Getenv("THINKGO_TEST_GO_STARTED"), []byte("started"), 0o600); err != nil {
		return 2
	}
	time.Sleep(fakeGoWait)
	return 0
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
	t.Setenv("THINKGO_TEST_FAKE_GO", "1")
	t.Setenv("THINKGO_TEST_FAKE_GO_STAGE", stage)
	t.Setenv("THINKGO_TEST_GO_STARTED", started)
	t.Setenv("THINKGO_TEST_PROJECT_BASE", base)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Run(ctx, base, []string{"list"}, io.Discard, io.Discard, nil) }()
	deadline := time.After(fakeGoWait)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("Go 工具尚未启动，发现流程已结束: %v", err)
		case <-deadline:
			t.Fatal("未观察到命令发现启动 Go 工具")
		case <-time.After(10 * time.Millisecond):
		}
	}
	begin := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后未返回 context.Canceled: %v", err)
		}
		if elapsed := time.Since(begin); elapsed > time.Second {
			t.Fatalf("取消后仍等待 Go 工具退出: %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后命令发现仍被 Go 工具阻塞")
	}
}
