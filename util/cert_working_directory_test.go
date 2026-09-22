//go:build !windows

package util

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateCertWithRemovedWorkingDirectory 验证发布目录被移除后，相对目标失败且不生成不完整证书。
func TestGenerateCertWithRemovedWorkingDirectory(t *testing.T) {
	output := t.TempDir()
	directory := t.TempDir()
	t.Chdir(directory)
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	for _, certificate := range []string{"cert.pem", filepath.Join(output, "cert.pem")} {
		if err := GenerateCert(certificate, "key.pem"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("丢失工作目录应明确失败: %v", err)
		}
	}
	if entries, err := os.ReadDir(output); err != nil || len(entries) != 0 {
		t.Fatalf("失败留下了证书: %v %v", entries, err)
	}
}
