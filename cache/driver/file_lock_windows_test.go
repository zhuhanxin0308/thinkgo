//go:build windows

package driver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestWindowsCacheLockHelperBoundaries 验证 Windows 锁平台辅助函数的清理、身份和退避边界。
func TestWindowsCacheLockHelperBoundaries(t *testing.T) {
	if got := nextCacheLockRemoveBackoff(time.Millisecond); got != 2*time.Millisecond {
		t.Fatalf("锁删除退避翻倍错误: %v", got)
	}
	if got := nextCacheLockRemoveBackoff(cacheLockRemoveMaxBackoff); got != cacheLockRemoveMaxBackoff {
		t.Fatalf("锁删除退避不应超过上限: %v", got)
	}
	if _, err := createWindowsCacheLockFile("\x00", windows.GENERIC_READ, windows.OPEN_EXISTING); err == nil {
		t.Fatal("非法 Windows 锁路径应返回错误")
	}

	path := filepath.Join(t.TempDir(), "lease.lock")
	handle, err := createCacheLockFile(path)
	if err != nil {
		t.Fatalf("创建 Windows 锁文件失败: %v", err)
	}
	info, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		t.Fatalf("读取 Windows 锁文件身份失败: %v", err)
	}
	if err = discardCreatedCacheLockFile(path, handle, info); err != nil {
		t.Fatalf("按创建句柄清理锁文件失败: %v", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("清理后的锁文件仍存在: %v", err)
	}
}
