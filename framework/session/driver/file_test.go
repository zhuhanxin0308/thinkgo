package driver

import (
	"os"
	"path/filepath"
	"testing"
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
