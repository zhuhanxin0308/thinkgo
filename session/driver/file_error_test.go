package driver

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSessionDriverErrorsExposeStableChineseMessages 验证公开哨兵错误保持稳定中文文本。
func TestSessionDriverErrorsExposeStableChineseMessages(t *testing.T) {
	for sessionErr, expected := range map[error]string{
		ErrInvalidSessionPath: "会话存储路径非法", ErrInvalidSessionID: "会话 ID 非法",
		ErrUnsafeSessionFile: "会话文件不安全", ErrSessionEntryTooLarge: "会话文件超过大小上限",
		ErrSessionLockTimeout: "会话文件锁超时", ErrInvalidSessionUpdate: "会话原子更新回调非法",
	} {
		if actual := sessionErr.Error(); actual != expected {
			t.Fatalf("会话驱动错误文本不稳定: expected=%q actual=%q", expected, actual)
		}
	}
}

// TestFileDeleteAndUpdateFailureSemantics 验证删除幂等、回调回滚、原子删除和大小上限。
func TestFileDeleteAndUpdateFailureSemantics(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	if err := driver.Write("target", "original"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := driver.Delete("target"); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := driver.Read("target"); err != nil || found {
		t.Fatalf("删除后 Session 仍存在: found=%t err=%v", found, err)
	}
	if err := driver.Update("atomic", func(string, bool) (string, bool, error) { return "original", false, nil }); err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("abort update")
	if err := driver.Update("atomic", func(string, bool) (string, bool, error) {
		return "changed", false, callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("回调错误未传播: %v", err)
	}
	if value, found, err := driver.Read("atomic"); err != nil || !found || value != "original" {
		t.Fatalf("失败回调污染原值: %q %t %v", value, found, err)
	}
	for _, key := range []string{"atomic", "missing"} {
		if err := driver.Update(key, func(string, bool) (string, bool, error) { return "", true, nil }); err != nil {
			t.Fatal(err)
		}
		if _, found, err := driver.Read(key); err != nil || found {
			t.Fatalf("原子删除后条目仍存在: %t %v", found, err)
		}
	}
	if err := driver.Update("oversized", func(string, bool) (string, bool, error) {
		return strings.Repeat("x", maxFileSessionEntryBytes+1), false, nil
	}); !errors.Is(err, ErrSessionEntryTooLarge) {
		t.Fatalf("超大更新未被拒绝: %v", err)
	}
	if err := driver.Update("invalid", nil); !errors.Is(err, ErrInvalidSessionUpdate) {
		t.Fatalf("nil 回调未被拒绝: %v", err)
	}
}

// TestFileClearHandlesTempsAndUnsafeManagedEntries 验证清理范围以及伪造路径拒绝。
func TestFileClearHandlesTempsAndUnsafeManagedEntries(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	oldTemp := filepath.Join(directory, ".tmp-session-old")
	recentTemp := filepath.Join(directory, ".tmp-session-recent")
	for _, path := range []string{oldTemp, recentTemp} {
		if err := os.WriteFile(path, []byte("temp"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-2 * fileSessionTempMaxAge)
	if err := os.Chtimes(oldTemp, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := driver.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("陈旧临时文件未删除: %v", err)
	}
	if _, err := os.Stat(recentTemp); err != nil {
		t.Fatalf("近期临时文件被删除: %v", err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, driver.sessionFilePath("forged")); err != nil {
		t.Skipf("当前环境不能创建符号链接: %v", err)
	}
	if err := driver.Clear(); !errors.Is(err, ErrUnsafeSessionFile) {
		t.Fatalf("伪造受管路径未被拒绝: %v", err)
	}
	if content, err := os.ReadFile(victim); err != nil || string(content) != "safe" {
		t.Fatalf("符号链接目标被修改: %q %v", content, err)
	}
}

// TestReadManagedFileReportsCurrentReplacementIdentity 保留类型替换和普通文件替换的身份安全回归。
func TestReadManagedFileReportsCurrentReplacementIdentity(t *testing.T) {
	for _, directoryTarget := range []bool{true, false} {
		t.Run(map[bool]string{true: "directory", false: "file"}[directoryTarget], func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "managed.session")
			backup := filepath.Join(directory, "backup")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			openAndReplace := func(currentPath string) (*os.File, error) {
				handle, err := openSessionFile(currentPath)
				if err != nil {
					return nil, err
				}
				if err = os.Rename(currentPath, backup); err == nil {
					if directoryTarget {
						err = os.Mkdir(currentPath, 0o700)
					} else {
						err = os.WriteFile(currentPath, []byte("new"), 0o600)
					}
				}
				if err != nil {
					_ = handle.Close()
					return nil, err
				}
				return handle, nil
			}
			_, info, found, err := readManagedFileWithOpen(path, maxFileSessionEntryBytes, openAndReplace)
			if !errors.Is(err, ErrUnsafeSessionFile) || found {
				t.Fatalf("身份替换未被拒绝: %t %v", found, err)
			}
			current, err := os.Lstat(path)
			if err != nil || info == nil || !os.SameFile(info, current) || info.IsDir() != directoryTarget {
				t.Fatalf("错误未携带当前替换身份: %#v %#v %v", info, current, err)
			}
		})
	}
}

type shortSessionWriter struct{}

func (shortSessionWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

type failingSessionWriter struct{ err error }

func (w failingSessionWriter) Write([]byte) (int, error) { return 0, w.err }

// TestFileHelpersRejectShortWritesAndInvalidManagedNames 验证短写与名称识别不会静默成功。
func TestFileHelpersRejectShortWritesAndInvalidManagedNames(t *testing.T) {
	if err := writeSessionData(shortSessionWriter{}, []byte("data")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("短写错误丢失: %v", err)
	}
	writeErr := errors.New("write failed")
	if err := writeSessionData(failingSessionWriter{err: writeErr}, []byte("data")); !errors.Is(err, writeErr) {
		t.Fatalf("写入错误丢失: %v", err)
	}
	for name, expected := range map[string]bool{
		hashedSessionFilename("id", ".session"): true,
		"other.txt":                             false, "sess_short.session": false,
		"sess_" + strings.Repeat("z", 64) + ".session": false,
	} {
		if actual := isManagedSessionFilename(name); actual != expected {
			t.Fatalf("受管名称识别错误: %s %t %t", name, actual, expected)
		}
	}
	if ignoreSessionNotExist(os.ErrNotExist) != nil || !errors.Is(ignoreSessionNotExist(writeErr), writeErr) {
		t.Fatal("缺失文件错误边界不正确")
	}
}
