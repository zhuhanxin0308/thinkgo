package util

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestCertificateCleanupIsIdempotentAndConfined 验证失败清理可重复执行，且不能删除根目录之外的文件。
func TestCertificateCleanupIsIdempotentAndConfined(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "certificates")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "sentinel")
	if err := os.WriteFile(outside, []byte("must-remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := removeRootFile(root, "missing.pem"); err != nil {
		t.Fatalf("缺失文件清理应幂等: %v", err)
	}
	if err := removeRootFile(root, filepath.Join("..", "sentinel")); err == nil {
		t.Fatal("清理越过了证书目录")
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "must-remain" {
		t.Fatal("清理影响了目录外文件")
	}
	file, err := os.CreateTemp(base, "closed-")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeAndSyncFile(file, []byte("private-data")); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("关闭文件的写入失败被吞掉: %v", err)
	}
	if content, err := os.ReadFile(file.Name()); err != nil || len(content) != 0 {
		t.Fatal("失败写入产生了错误的私钥内容")
	}
}
