package driver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileRejectsTraversalRead 验证文件型 Session 驱动会拒绝目录穿越读取。
func TestFileRejectsTraversalRead(t *testing.T) {
	basePath := t.TempDir()
	sessionDir := filepath.Join(basePath, "session")
	driver := NewFile(sessionDir)

	secretPath := filepath.Join(basePath, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	traversalID := filepath.Join("..", "secret.txt")
	content, err := driver.Read(traversalID)
	if err == nil {
		t.Fatalf("目录穿越读取应被拒绝，实际读取到 %q", content)
	}
}

// TestFileRejectsTraversalWriteAndDelete 验证文件型 Session 驱动不会越权写入或删除目录外文件。
func TestFileRejectsTraversalWriteAndDelete(t *testing.T) {
	basePath := t.TempDir()
	sessionDir := filepath.Join(basePath, "session")
	driver := NewFile(sessionDir)

	victimPath := filepath.Join(basePath, "victim.txt")
	if err := os.WriteFile(victimPath, []byte("origin"), 0o600); err != nil {
		t.Fatalf("写入受保护文件失败: %v", err)
	}

	traversalID := filepath.Join("..", "victim.txt")
	if err := driver.Write(traversalID, "hacked"); err == nil {
		t.Fatal("目录穿越写入应被拒绝")
	}

	content, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatalf("读取受保护文件失败: %v", err)
	}
	if string(content) != "origin" {
		t.Fatalf("目录穿越写入不应修改目录外文件，实际内容为 %q", string(content))
	}

	if err := driver.Delete(traversalID); err == nil {
		t.Fatal("目录穿越删除应被拒绝")
	}

	if _, err := os.Stat(victimPath); err != nil {
		t.Fatalf("目录穿越删除不应移除目录外文件: %v", err)
	}
}

// TestFileDeleteMissingSessionIsIdempotent 验证删除不存在的合法 Session 不应返回错误。
func TestFileDeleteMissingSessionIsIdempotent(t *testing.T) {
	driver := NewFile(t.TempDir())

	if err := driver.Delete("missing-session-id"); err != nil {
		t.Fatalf("删除不存在的 Session 应保持幂等，实际错误: %v", err)
	}
}

// TestFileGCKeepsUnmanagedFiles 验证 GC 只回收合法 Session 文件，不误删目录内其它文件。
func TestFileGCKeepsUnmanagedFiles(t *testing.T) {
	dir := t.TempDir()
	driver := NewFile(dir)

	unmanagedPath := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(unmanagedPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("写入非 Session 文件失败: %v", err)
	}
	if err := driver.Write("managed-session", "data"); err != nil {
		t.Fatalf("写入 Session 文件失败: %v", err)
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(unmanagedPath, oldTime, oldTime); err != nil {
		t.Fatalf("设置非 Session 文件时间失败: %v", err)
	}
	if err := os.Chtimes(filepath.Join(dir, "managed-session"), oldTime, oldTime); err != nil {
		t.Fatalf("设置 Session 文件时间失败: %v", err)
	}

	removed, err := driver.GC(time.Hour)
	if err != nil {
		t.Fatalf("GC 不应返回错误: %v", err)
	}
	if removed != 1 {
		t.Fatalf("GC 应只删除 1 个合法 Session 文件，实际删除 %d 个", removed)
	}
	if content, err := os.ReadFile(unmanagedPath); err != nil || string(content) != "keep" {
		t.Fatalf("GC 不应删除非 Session 文件，content=%q err=%v", string(content), err)
	}
}
