package driver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFileAcquireLockRejectsUnsafeTargets 验证锁路径被目录、超大文件或符号链接占用时不会继续操作。
func TestFileAcquireLockRejectsUnsafeTargets(t *testing.T) {
	driver, directory := newTestFileDriver(t)

	t.Run("directory", func(t *testing.T) {
		path := driver.lockFilePath("directory-target")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("创建目录锁占位失败: %v", err)
		}
		if _, err := driver.acquireCrossProcessLock("directory-target", "owner"); !errors.Is(err, ErrUnsafeSessionFile) {
			t.Fatalf("目录锁占位应被拒绝，实际错误为 %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := driver.lockFilePath("oversized-target")
		data := []byte(strings.Repeat("x", maxFileSessionLockBytes+1))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("创建超大锁文件失败: %v", err)
		}
		if _, err := driver.acquireCrossProcessLock("oversized-target", "owner"); !errors.Is(err, ErrUnsafeSessionFile) {
			t.Fatalf("超大锁文件应被拒绝，实际错误为 %v", err)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		missingDriver, missingDirectory := newTestFileDriver(t)
		if err := os.RemoveAll(missingDirectory); err != nil {
			t.Fatalf("移除临时 Session 目录失败: %v", err)
		}
		if _, err := missingDriver.acquireCrossProcessLock("missing-parent", "owner"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("缺失锁目录应返回路径错误，实际错误为 %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(directory, "lock-target")
		path := driver.lockFilePath("symlink-target")
		if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
			t.Fatalf("创建符号链接目标失败: %v", err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("当前环境不支持创建符号链接: %v", err)
		}
		if _, err := driver.acquireCrossProcessLock("symlink-target", "owner"); !errors.Is(err, ErrUnsafeSessionFile) {
			t.Fatalf("符号链接锁文件应被拒绝，实际错误为 %v", err)
		}
	})
}

func TestFileHelperBranchesAndErrorBoundaries(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	if err := driver.validate("valid"); err != nil {
		t.Fatalf("有效 Session ID 不应失败: %v", err)
	}
	var nilDriver *File
	if !errors.Is(nilDriver.validate("id"), ErrInvalidSessionPath) {
		t.Fatal("nil 文件驱动应返回路径错误")
	}
	if _, err := nilDriver.GC(time.Hour); !errors.Is(err, ErrInvalidSessionPath) {
		t.Fatalf("nil 文件驱动 GC 应返回路径错误: %v", err)
	}
	if containsControlCharacter("safe") || !containsControlCharacter("unsafe\n") {
		t.Fatal("控制字符检测结果错误")
	}

	if removed, err := driver.GC(0); err != nil || removed != 0 {
		t.Fatalf("非正 GC 生命周期应为空操作: removed=%d err=%v", removed, err)
	}

	regularPath := filepath.Join(directory, "regular.session")
	if err := os.WriteFile(regularPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("创建受管文件失败: %v", err)
	}
	openFailure := errors.New("open failure")
	if _, _, found, err := readManagedFileWithOpen(regularPath, maxFileSessionEntryBytes, func(string) (*os.File, error) {
		return nil, openFailure
	}); !errors.Is(err, openFailure) || found {
		t.Fatalf("打开错误应被原样返回: found=%t err=%v", found, err)
	}
	if _, _, found, err := readManagedFileWithOpen(regularPath, maxFileSessionEntryBytes, func(path string) (*os.File, error) {
		handle, openErr := os.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		_ = handle.Close()
		return handle, nil
	}); !errors.Is(err, ErrUnsafeSessionFile) || found {
		t.Fatalf("关闭句柄的 Stat 错误应被安全包装: found=%t err=%v", found, err)
	}
	otherPath := filepath.Join(directory, "other.session")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatalf("创建替代文件失败: %v", err)
	}
	if _, _, found, err := readManagedFileWithOpen(regularPath, maxFileSessionEntryBytes, func(string) (*os.File, error) {
		return os.Open(otherPath)
	}); !errors.Is(err, ErrUnsafeSessionFile) || found {
		t.Fatalf("文件身份替换应被拒绝: found=%t err=%v", found, err)
	}
	oversizedPath := filepath.Join(directory, "oversized.session")
	if err := os.WriteFile(oversizedPath, []byte(strings.Repeat("x", maxFileSessionEntryBytes+1)), 0o600); err != nil {
		t.Fatalf("创建超大文件失败: %v", err)
	}
	if _, _, found, err := readManagedFileWithOpen(oversizedPath, maxFileSessionEntryBytes, os.Open); !errors.Is(err, ErrSessionEntryTooLarge) || found {
		t.Fatalf("超大 Session 文件应被拒绝: found=%t err=%v", found, err)
	}

	missingTarget := filepath.Join(directory, "missing", "target.session")
	if err := driver.writeManagedFile(missingTarget, []byte("data")); err == nil {
		t.Fatal("目标父目录不存在时原子替换应返回错误")
	}
	targetPath := driver.sessionFilePath("remove-helper")
	if err := os.WriteFile(targetPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("创建删除辅助文件失败: %v", err)
	}
	info, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("读取删除辅助文件身份失败: %v", err)
	}
	if removed, err := removeSessionFileIfSame(targetPath, nil); err != nil || removed {
		t.Fatalf("缺少期望身份时不应删除文件: removed=%t err=%v", removed, err)
	}
	otherInfo, err := os.Lstat(otherPath)
	if err != nil {
		t.Fatalf("读取替代文件身份失败: %v", err)
	}
	if removed, err := removeSessionFileIfSame(targetPath, otherInfo); err != nil || removed {
		t.Fatalf("身份不匹配时不应删除文件: removed=%t err=%v", removed, err)
	}
	if removed, err := removeSessionFileIfSame(targetPath, info); err != nil || !removed {
		t.Fatalf("身份匹配时应删除文件: removed=%t err=%v", removed, err)
	}
	if removed, err := removeSessionFileIfSame(targetPath, info); err != nil || removed {
		t.Fatalf("重复删除应幂等: removed=%t err=%v", removed, err)
	}
}

func TestFileLockPureHelpersAndOwnerMatching(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	info, err := driver.acquireCrossProcessLock("helper-lock", "owner")
	if err != nil {
		t.Fatalf("创建辅助锁失败: %v", err)
	}
	t.Cleanup(func() { _, _ = removeOwnedLockFile(driver.lockFilePath("helper-lock"), "owner", info) })
	if matched, err := lockFileMatchesOwner(driver.lockFilePath("helper-lock"), "owner", info); err != nil || !matched {
		t.Fatalf("owner 匹配失败: matched=%t err=%v", matched, err)
	}
	if matched, err := lockFileMatchesOwner(driver.lockFilePath("helper-lock"), "wrong", info); err != nil || matched {
		t.Fatalf("错误 owner 不应匹配: matched=%t err=%v", matched, err)
	}
	if matched, err := lockFileMatchesOwner(driver.lockFilePath("helper-lock"), "", info); err != nil || !matched {
		t.Fatalf("空 owner 应仅校验文件身份: matched=%t err=%v", matched, err)
	}
	if matched, err := lockFileMatchesOwner(driver.lockFilePath("helper-lock"), "owner", nil); err != nil || matched {
		t.Fatalf("缺少期望身份时不应匹配: matched=%t err=%v", matched, err)
	}
	if matched, err := lockFileMatchesOwner(filepath.Join(driver.path, "missing.lock"), "owner", info); err != nil || matched {
		t.Fatalf("缺少锁文件应返回未匹配: matched=%t err=%v", matched, err)
	}

	for _, test := range []struct {
		name     string
		info     os.FileInfo
		found    bool
		err      error
		expected bool
		previous error
	}{
		{name: "missing", found: false, expected: true},
		{name: "not found error", err: os.ErrNotExist, expected: true},
		{name: "unsafe regular", info: info, err: ErrUnsafeSessionFile, expected: true},
		{name: "stable", found: true, expected: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := lockObservationMayBeTransient(test.info, test.found, test.err); got != test.expected {
				t.Fatalf("锁观察瞬态判断错误: got=%t want=%t", got, test.expected)
			}
		})
	}

	previous := errors.New("previous access denied")
	if got := fileSessionLockAccessDeniedAfterRead(previous, true, nil); got != nil {
		t.Fatalf("读取到稳定锁后应清除旧拒绝错误: %v", got)
	}
	if got := fileSessionLockAccessDeniedAfterRead(previous, false, nil); got != previous {
		t.Fatalf("未读到锁时应保留旧拒绝错误: %v", got)
	}
	if got := fileSessionLockAccessDeniedAfterRead(previous, false, os.ErrNotExist); got != previous {
		t.Fatalf("普通读取错误不应覆盖旧拒绝错误: %v", got)
	}

	owner, err := newSessionLockOwner()
	if err != nil || owner == "" {
		t.Fatalf("生成锁 owner 失败: %q %v", owner, err)
	}
	if _, err := base64.RawURLEncoding.DecodeString(owner); err != nil {
		t.Fatalf("锁 owner 应为无填充 Base64: %v", err)
	}
	for _, test := range []struct {
		data []byte
		err  error
		want bool
	}{
		{data: []byte("{"), err: &json.SyntaxError{Offset: 1}, want: true},
		{data: []byte("{}"), err: errors.New("invalid"), want: false},
		{data: []byte("{}"), err: nil, want: false},
	} {
		if got := lockPayloadMayBeIncomplete(test.data, test.err); got != test.want {
			t.Fatalf("锁载荷完整性判断错误: data=%q got=%t want=%t", test.data, got, test.want)
		}
	}
}
