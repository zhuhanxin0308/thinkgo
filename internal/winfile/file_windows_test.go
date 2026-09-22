//go:build windows

package winfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestReplaceWhileReaderRetainsOldFile 验证读者保留旧文件句柄时路径仍可原子指向新内容。
func TestReplaceWhileReaderRetainsOldFile(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "data")
	source := filepath.Join(directory, "replacement")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(target, windows.GENERIC_READ, windows.OPEN_EXISTING)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(source, target); err != nil {
		t.Fatalf("共享删除读句柄阻断原子替换: %v", err)
	}
	if current, err := os.ReadFile(target); err != nil || string(current) != "new" {
		t.Fatalf("替换后的路径错误: %q %v", current, err)
	}
	if old, err := io.ReadAll(reader); err != nil || string(old) != "old" {
		t.Fatalf("已打开的读者失去原内容: %q %v", old, err)
	}
}

// TestRemovalKeepsReplacement 验证替换路径后，旧身份不能删除新内容。
func TestRemovalKeepsReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "data")
	handle, err := Open(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.CREATE_NEW)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	old, err := handle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(replacement, path); err != nil {
		t.Fatal(err)
	}
	handle, err = Open(path, windows.GENERIC_READ|windows.DELETE, windows.OPEN_EXISTING)
	if err != nil {
		t.Fatal(err)
	}
	if _, matches, err := Matches(path, handle, old); err != nil || matches {
		t.Fatalf("旧句柄匹配了替换文件: %v %v", matches, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveIfSame(path, old); err != nil || removed {
		t.Fatalf("删除了替换文件: %v %v", removed, err)
	}
	if removed, err := RemoveIfSame(path, nil); err != nil || removed {
		t.Fatalf("无身份时删除文件: %v %v", removed, err)
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveIfSame(path, current); err != nil || !removed {
		t.Fatalf("删除当前身份失败: %v %v", removed, err)
	}
	if removed, err := RemoveIfSame(path, current); err != nil || removed {
		t.Fatalf("不存在文件重复删除: %v %v", removed, err)
	}
}

// TestFileErrorsAndUnsafeHandles 验证原生错误链、关闭句柄和目录身份均不伪装成删除成功。
func TestFileErrorsAndUnsafeHandles(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing")
	for _, path := range []string{missing, "bad\x00path"} {
		_, err := Open(path, windows.GENERIC_READ, windows.OPEN_EXISTING)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != "open" || pathErr.Path != path {
			t.Fatalf("打开错误缺少路径: %v", err)
		}
	}
	if removed, err := RemoveIfSame("bad\x00path", nil); err == nil || removed {
		t.Fatalf("无效路径错误丢失: %v %v", removed, err)
	}
	var linkErr *os.LinkError
	if err := Replace(missing, filepath.Join(directory, "target")); !errors.As(err, &linkErr) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("替换错误链丢失: %v", err)
	}
	handle, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Matches(directory, handle, nil); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("目录被接受: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Matches(directory, handle, nil); err == nil {
		t.Fatal("关闭句柄未返回错误")
	}
	if err := Delete(handle); err == nil {
		t.Fatal("关闭句柄删除未报错")
	}
}
