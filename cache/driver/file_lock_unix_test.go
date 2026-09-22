//go:build (linux && !android) || (darwin && !ios)

package driver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestUnixCacheLockRejectsUnavailableHandles 验证租约加锁和解锁均拒绝空句柄及已关闭句柄。
func TestUnixCacheLockRejectsUnavailableHandles(t *testing.T) {
	handle, err := createCacheLockFile(filepath.Join(t.TempDir(), "closed.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		name string
		run  func(*os.File) error
	}{
		{name: "加锁", run: lockCacheLockFile},
		{name: "解锁", run: unlockCacheLockFile},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(nil); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("空句柄错误丢失: %v", err)
			}
			if err := operation.run(handle); err == nil {
				t.Fatal("关闭句柄错误丢失")
			}
		})
	}
}

// TestUnixCreatedLockCleanupPreservesIdentity 验证 Unix 创建失败清理只删除本次创建的身份并关闭所有句柄。
func TestUnixCreatedLockCleanupPreservesIdentity(t *testing.T) {
	if err := discardCreatedCacheLockFile("missing", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"matched", "missing identity", "replaced", "closed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "created.lock")
			handle, err := createCacheLockFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := lockCacheLockFile(handle); err != nil {
				_ = handle.Close()
				t.Fatal(err)
			}
			expected, err := handle.Stat()
			if err != nil {
				_ = handle.Close()
				t.Fatal(err)
			}
			switch mode {
			case "missing identity":
				expected = nil
			case "replaced":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("new owner"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if err := handle.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err = discardCreatedCacheLockFile(path, handle, expected)
			if mode == "closed" && err == nil || mode != "closed" && err != nil {
				t.Fatalf("清理关闭错误边界错误: %v", err)
			}
			if _, err := handle.Stat(); err == nil {
				t.Fatal("清理泄漏句柄")
			}
			_, statErr := os.Stat(path)
			if mode == "matched" && !errors.Is(statErr, os.ErrNotExist) || mode != "matched" && statErr != nil {
				t.Fatalf("清理删除了不匹配的身份: %v", statErr)
			}
			if mode == "replaced" {
				if data, err := os.ReadFile(path); err != nil || string(data) != "new owner" {
					t.Fatalf("清理破坏替换后的文件: %q %v", data, err)
				}
			}
		})
	}
}

// TestUnixLockedRemovalAndDataIdentity 验证锁内删除幂等以及文件删除不能跟随符号链接。
func TestUnixLockedRemovalAndDataIdentity(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "owned.lock")
	handle, err := createCacheLockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := lockCacheLockFile(handle); err != nil {
		t.Fatal(err)
	}
	defer unlockCacheLockFile(handle)
	info, err := handle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := removeLockedCacheLockFile(path, handle, nil); err != nil || removed {
		t.Fatalf("缺失身份仍删除锁: %t %v", removed, err)
	}
	if removed, err := removeLockedCacheLockFile(path, handle, info); err != nil || !removed {
		t.Fatalf("删除自有锁失败: %t %v", removed, err)
	}
	if removed, err := removeLockedCacheLockFile(path, handle, info); err != nil || removed {
		t.Fatalf("锁删除不是幂等的: %t %v", removed, err)
	}
	target, link := filepath.Join(directory, "target"), filepath.Join(directory, "link")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if removed, err := removeFileIfSame(link, info); !errors.Is(err, ErrUnsafeCacheEntry) || removed {
		t.Fatalf("删除跟随了符号链接: %t %v", removed, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "safe" {
		t.Fatalf("删除破坏链接目标: %q %v", data, err)
	}
}
