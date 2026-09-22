package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalDiskCoversThinkPHPFilesystemOperations 验证本地磁盘的写入、读取、
// 元数据、目录、复制、移动、列表、可见性和删除 API。
func TestLocalDiskCoversThinkPHPFilesystemOperations(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "storage")
	disk, err := NewLocal(LocalConfig{Root: rootPath, URL: "/storage", Visibility: VisibilityPublic})
	if err != nil {
		t.Fatalf("创建本地磁盘失败: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })

	if err := disk.Write("reports/daily.txt", "ThinkPHP"); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
	if content, err := disk.Read("reports/daily.txt"); err != nil || content != "ThinkPHP" {
		t.Fatalf("读取文件错误: content=%q err=%v", content, err)
	}
	if exists, err := disk.FileExists("reports/daily.txt"); err != nil || !exists {
		t.Fatalf("文件存在检查错误: exists=%t err=%v", exists, err)
	}
	if exists, err := disk.DirectoryExists("reports"); err != nil || !exists {
		t.Fatalf("目录存在检查错误: exists=%t err=%v", exists, err)
	}
	if size, err := disk.FileSize("reports/daily.txt"); err != nil || size != int64(len("ThinkPHP")) {
		t.Fatalf("文件大小错误: size=%d err=%v", size, err)
	}
	if modified, err := disk.LastModified("reports/daily.txt"); err != nil || modified <= 0 {
		t.Fatalf("修改时间错误: modified=%v err=%v", modified, err)
	}
	if mimeType, err := disk.MimeType("reports/daily.txt"); err != nil || !strings.HasPrefix(mimeType, "text/plain") {
		t.Fatalf("MIME 类型错误: type=%q err=%v", mimeType, err)
	}
	if err := disk.Copy("reports/daily.txt", "archive/copy.txt"); err != nil {
		t.Fatalf("复制文件失败: %v", err)
	}
	if err := disk.Move("archive/copy.txt", "archive/final.txt"); err != nil {
		t.Fatalf("移动文件失败: %v", err)
	}
	entries, err := disk.ListContents("", true)
	if err != nil {
		t.Fatalf("列出磁盘内容失败: %v", err)
	}
	if !containsFilesystemEntry(entries, "reports/daily.txt") || !containsFilesystemEntry(entries, "archive/final.txt") {
		t.Fatalf("递归列表缺少文件: %#v", entries)
	}
	if visibility, err := disk.Visibility("archive/final.txt"); err != nil || visibility != VisibilityPublic {
		t.Fatalf("默认可见性错误: visibility=%q err=%v", visibility, err)
	}
	if err := disk.SetVisibility("archive/final.txt", VisibilityPrivate); err != nil {
		t.Fatalf("设置可见性失败: %v", err)
	}
	if err := disk.Delete("archive/final.txt"); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	if err := disk.DeleteDirectory("archive"); err != nil {
		t.Fatalf("删除目录失败: %v", err)
	}
	if got, err := disk.Path("reports/daily.txt"); err != nil || got != filepath.Join(rootPath, "reports", "daily.txt") {
		t.Fatalf("完整路径错误: path=%q err=%v", got, err)
	}
}

// TestLocalDiskRejectsTraversalAndLinks 验证业务路径不能离开磁盘根目录，
// 且默认行为与 ThinkPHP Local 的 DISALLOW_LINKS 一致拒绝符号链接。
func TestLocalDiskRejectsTraversalAndLinks(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "storage")
	disk, err := NewLocal(LocalConfig{Root: rootPath})
	if err != nil {
		t.Fatalf("创建本地磁盘失败: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })

	for _, unsafePath := range []string{"../secret.txt", "safe/../../secret.txt", "safe\x00name"} {
		if err := disk.Write(unsafePath, "unsafe"); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("危险路径 %q 应被拒绝: %v", unsafePath, err)
		}
	}
	if err := disk.Write("/normalized/../inside.txt", "inside"); err != nil {
		t.Fatalf("ThinkPHP 风格相对路径规范化失败: %v", err)
	}
	if content, err := disk.Read("inside.txt"); err != nil || content != "inside" {
		t.Fatalf("规范化路径未落在磁盘根目录: content=%q err=%v", content, err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("写入外部测试文件失败: %v", err)
	}
	linkPath := filepath.Join(rootPath, "linked")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("当前环境不允许创建符号链接: %v", err)
	}
	if _, err := disk.ListContents("", true); !errors.Is(err, ErrSymbolicLink) {
		t.Fatalf("默认目录列表应拒绝符号链接: %v", err)
	}
}

func containsFilesystemEntry(entries []Entry, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}
