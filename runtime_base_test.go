package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSelectRuntimeBasePathPrefersExecutableBundle 验证发布包从包外启动时使用可执行文件目录。
func TestSelectRuntimeBasePathPrefersExecutableBundle(t *testing.T) {
	cwd := t.TempDir()
	executableDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(executableDir, "config"), 0o755); err != nil {
		t.Fatalf("创建发布配置目录失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(executableDir, "app"), 0o755); err != nil {
		t.Fatalf("创建发布应用目录失败: %v", err)
	}

	got := selectRuntimeBasePath(cwd, filepath.Join(executableDir, "thinkgo.exe"))
	if got != executableDir {
		t.Fatalf("包外启动必须选择可执行文件目录: got=%q want=%q", got, executableDir)
	}
}

// TestSelectRuntimeBasePathUsesWorkingDirectoryForGoRun 验证 go run 的临时可执行文件不存在包布局时仍使用当前目录。
func TestSelectRuntimeBasePathUsesWorkingDirectoryForGoRun(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "config"), 0o755); err != nil {
		t.Fatalf("创建开发配置目录失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "app"), 0o755); err != nil {
		t.Fatalf("创建开发应用目录失败: %v", err)
	}

	got := selectRuntimeBasePath(cwd, filepath.Join(t.TempDir(), "go-build", "thinkgo.exe"))
	if got != cwd {
		t.Fatalf("开发运行必须选择当前目录: got=%q want=%q", got, cwd)
	}
}

// TestSelectRuntimeBasePathFallsBackToWorkingDirectory 验证两个位置都没有包布局时保留当前目录并交给上层报错。
func TestSelectRuntimeBasePathFallsBackToWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	got := selectRuntimeBasePath(cwd, filepath.Join(t.TempDir(), "thinkgo.exe"))
	if got != cwd {
		t.Fatalf("缺少包布局时应保留当前目录: got=%q want=%q", got, cwd)
	}
}
