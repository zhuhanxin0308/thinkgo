//go:build windows

package driver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsSessionErrorsKeepOperationPaths 验证 Windows 原生错误保留实际操作与路径，便于定位跨进程失败。
func TestWindowsSessionErrorsKeepOperationPaths(t *testing.T) {
	directory := t.TempDir()
	source, target := filepath.Join(directory, "missing"), filepath.Join(directory, "target")
	var link *os.LinkError
	if err := replaceSessionFile(source, target); !errors.As(err, &link) || link.Op != "rename" || link.Old != source || link.New != target || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("替换错误缺少路径: %T %v", err, err)
	}
	var path *os.PathError
	if err := restrictSessionPath(source, false); !errors.As(err, &path) || path.Op != "chmod" || path.Path != source || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("权限错误缺少路径: %T %v", err, err)
	}
}
