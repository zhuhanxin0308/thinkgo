package driver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFileAcquireLockRejectsUnsafeTargets 验证稳定锁路径的目录、符号链接和缺失目录错误。
func TestFileAcquireLockRejectsUnsafeTargets(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "missing parent"} {
		t.Run(kind, func(t *testing.T) {
			driver, directory := newTestFileDriver(t)
			path := driver.mutationGuardPath("target")
			want := ErrUnsafeSessionFile
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(directory, "target")
				if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("当前环境不能创建符号链接: %v", err)
				}
			case "missing parent":
				if err := os.Remove(directory); err != nil {
					t.Fatal(err)
				}
				want = os.ErrNotExist
			}
			if err := driver.Write("target", "data"); !errors.Is(err, want) {
				t.Fatalf("不安全或缺失锁路径未被拒绝: %v", err)
			}
		})
	}
}

// TestFileHelperBranchesAndErrorBoundaries 保留读写、身份与删除的真实错误边界覆盖。
func TestFileHelperBranchesAndErrorBoundaries(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	if err := driver.validate("valid"); err != nil {
		t.Fatal(err)
	}
	var nilDriver *File
	if !errors.Is(nilDriver.validate("id"), ErrInvalidSessionPath) {
		t.Fatal("nil 驱动未拒绝操作")
	}
	if _, err := nilDriver.GC(time.Hour); !errors.Is(err, ErrInvalidSessionPath) {
		t.Fatal(err)
	}
	if containsControlCharacter("safe") || !containsControlCharacter("unsafe\n") {
		t.Fatal("控制字符判断错误")
	}
	if removed, err := driver.GC(0); err != nil || removed != 0 {
		t.Fatalf("非正生命周期 GC 错误: %d %v", removed, err)
	}
	regularPath := filepath.Join(directory, "regular.session")
	if err := os.WriteFile(regularPath, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	openFailure := errors.New("open failure")
	if _, _, found, err := readManagedFileWithOpen(regularPath, maxFileSessionEntryBytes, func(string) (*os.File, error) {
		return nil, openFailure
	}); !errors.Is(err, openFailure) || found {
		t.Fatalf("打开错误未传播: %t %v", found, err)
	}
	if _, _, found, err := readManagedFileWithOpen(regularPath, maxFileSessionEntryBytes, func(path string) (*os.File, error) {
		handle, err := os.Open(path)
		if err == nil {
			_ = handle.Close()
		}
		return handle, err
	}); !errors.Is(err, ErrUnsafeSessionFile) || found {
		t.Fatalf("关闭句柄未被拒绝: %t %v", found, err)
	}
	otherPath := filepath.Join(directory, "other.session")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := readManagedFileWithOpen(regularPath, maxFileSessionEntryBytes, func(string) (*os.File, error) {
		return os.Open(otherPath)
	}); !errors.Is(err, ErrUnsafeSessionFile) || found {
		t.Fatalf("身份替换未被拒绝: %t %v", found, err)
	}
	oversizedPath := filepath.Join(directory, "oversized.session")
	if err := os.WriteFile(oversizedPath, []byte(strings.Repeat("x", maxFileSessionEntryBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := readManagedFileWithOpen(oversizedPath, maxFileSessionEntryBytes, os.Open); !errors.Is(err, ErrSessionEntryTooLarge) || found {
		t.Fatalf("超大文件未被拒绝: %t %v", found, err)
	}
	if err := driver.writeManagedFile(filepath.Join(directory, "missing", "target.session"), []byte("data")); err == nil {
		t.Fatal("目标目录缺失时替换伪装成功")
	}
	targetPath := driver.sessionFilePath("remove-helper")
	if err := os.WriteFile(targetPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Lstat(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []os.FileInfo{nil, otherInfo} {
		if removed, err := removeSessionFileIfSame(targetPath, expected); err != nil || removed {
			t.Fatalf("缺失或错误身份删除了文件: %t %v", removed, err)
		}
	}
	if removed, err := removeSessionFileIfSame(targetPath, info); err != nil || !removed {
		t.Fatalf("删除身份匹配文件失败: %t %v", removed, err)
	}
	if removed, err := removeSessionFileIfSame(targetPath, info); err != nil || removed {
		t.Fatalf("重复删除错误: %t %v", removed, err)
	}
}
