package driver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSessionRootRejectsInvalidTargetsAndSupportsReadWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	for _, path := range []string{"", "../escape", filepath.Join(t.TempDir(), "absolute"), "bad\x00name"} {
		if store, err := NewFileInRoot(root, path); store != nil || !errors.Is(err, ErrInvalidSessionPath) {
			t.Errorf("无效相对路径应被拒绝: %q, %v", path, err)
		}
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝非法路径前不应创建运行目录: %v", err)
	}
	store, err := NewFileInRoot(root, filepath.Join("session", "nested"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "6789bcdef0123456789abcdef0123456"
	if err := store.Write(id, "payload"); err != nil {
		t.Fatal(err)
	}
	if value, found, err := store.Read(id); err != nil || !found || value != "payload" {
		t.Fatalf("受限根目录中的会话应可读写: %q, %v, %v", value, found, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := NewFileInRoot(root, "file"); store != nil || !errors.Is(err, ErrInvalidSessionPath) {
		t.Fatalf("普通文件不能作为会话目录: %v", err)
	}
}

func TestFileSessionDirectoryIdentityRejectsReplacedTarget(t *testing.T) {
	original := t.TempDir()
	directory, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	original, err = filepath.EvalSymlinks(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyFileSessionDirectory(directory, original); err != nil {
		t.Fatal(err)
	}
	replacement := t.TempDir()
	if _, err := verifyFileSessionDirectory(directory, replacement); err == nil {
		t.Fatal("已打开的目录不能与后来出现的目录混用")
	}
}
