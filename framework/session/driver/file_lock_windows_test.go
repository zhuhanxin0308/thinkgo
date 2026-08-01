//go:build windows

package driver

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// writeWindowsLockFixture 创建具有稳定 owner 的 Windows 锁文件测试夹具。
func writeWindowsLockFixture(t *testing.T, path, owner string) os.FileInfo {
	t.Helper()
	payload, err := json.Marshal(fileSessionLock{Owner: owner, ExpireAt: time.Now().Add(time.Minute).UnixNano()})
	if err != nil {
		t.Fatalf("编码锁文件失败: %v", err)
	}
	handle, err := createLockFile(path)
	if err != nil {
		t.Fatalf("创建锁文件失败: %v", err)
	}
	if _, err = handle.Write(payload); err != nil {
		_ = handle.Close()
		t.Fatalf("写入锁文件失败: %v", err)
	}
	if err = handle.Close(); err != nil {
		t.Fatalf("关闭锁文件失败: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("读取锁文件身份失败: %v", err)
	}
	return info
}

// TestFileLockAllowsDeletionWhileSharedHandleIsOpen 验证锁句柄声明删除共享后不会阻塞正常释放。
func TestFileLockAllowsDeletionWhileSharedHandleIsOpen(t *testing.T) {
	_, directory := newTestFileDriver(t)
	path := filepath.Join(directory, "shared-delete.lock")
	expected := writeWindowsLockFixture(t, path, "owner")
	held, err := openLockFile(path)
	if err != nil {
		t.Fatalf("打开共享锁句柄失败: %v", err)
	}
	deleted, err := removeOwnedLockFile(path, "owner", expected)
	if err != nil {
		_ = held.Close()
		t.Fatalf("存在共享句柄时删除锁失败: %v", err)
	}
	if !deleted {
		_ = held.Close()
		t.Fatal("仍归当前 owner 的锁应被删除")
	}
	if err = held.Close(); err != nil {
		t.Fatalf("关闭共享锁句柄失败: %v", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("释放后锁文件仍存在: %v", err)
	}
}

// TestFileLockRetriesOnlySharingViolations 验证共享冲突有界退避，其他错误立即返回。
func TestFileLockRetriesOnlySharingViolations(t *testing.T) {
	_, directory := newTestFileDriver(t)
	t.Run("共享冲突后成功", func(t *testing.T) {
		path := filepath.Join(directory, "retry-success.lock")
		expected := writeWindowsLockFixture(t, path, "owner")
		originalDelete := windowsDeleteLockHandle
		originalSleep := windowsLockRetrySleep
		defer func() {
			windowsDeleteLockHandle = originalDelete
			windowsLockRetrySleep = originalSleep
		}()
		calls := 0
		var waits []time.Duration
		windowsDeleteLockHandle = func(handle *os.File) error {
			calls++
			if calls < 3 {
				return windows.ERROR_SHARING_VIOLATION
			}
			return originalDelete(handle)
		}
		windowsLockRetrySleep = func(wait time.Duration) {
			waits = append(waits, wait)
		}
		deleted, err := removeOwnedLockFile(path, "owner", expected)
		if err != nil || !deleted {
			t.Fatalf("共享冲突重试后删除失败: deleted=%t err=%v", deleted, err)
		}
		if calls != 3 || len(waits) != 2 || waits[0] != fileLockRemoveInitialBackoff || waits[1] != 2*fileLockRemoveInitialBackoff {
			t.Fatalf("退避序列错误: calls=%d waits=%v", calls, waits)
		}
	})

	t.Run("非共享错误立即返回", func(t *testing.T) {
		path := filepath.Join(directory, "no-retry.lock")
		expected := writeWindowsLockFixture(t, path, "owner")
		originalDelete := windowsDeleteLockHandle
		originalSleep := windowsLockRetrySleep
		defer func() {
			windowsDeleteLockHandle = originalDelete
			windowsLockRetrySleep = originalSleep
		}()
		calls := 0
		waits := 0
		windowsDeleteLockHandle = func(*os.File) error {
			calls++
			return windows.ERROR_ACCESS_DENIED
		}
		windowsLockRetrySleep = func(time.Duration) {
			waits++
		}
		_, err := removeOwnedLockFile(path, "owner", expected)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != "remove" || pathErr.Path != path || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("非共享错误包装错误: %T %v", err, err)
		}
		if calls != 1 || waits != 0 {
			t.Fatalf("非共享错误不应重试: calls=%d waits=%d", calls, waits)
		}
	})

	t.Run("达到重试上限", func(t *testing.T) {
		path := filepath.Join(directory, "retry-limit.lock")
		expected := writeWindowsLockFixture(t, path, "owner")
		originalDelete := windowsDeleteLockHandle
		originalSleep := windowsLockRetrySleep
		defer func() {
			windowsDeleteLockHandle = originalDelete
			windowsLockRetrySleep = originalSleep
		}()
		calls := 0
		waits := 0
		windowsDeleteLockHandle = func(*os.File) error {
			calls++
			return windows.ERROR_SHARING_VIOLATION
		}
		windowsLockRetrySleep = func(time.Duration) {
			waits++
		}
		_, err := removeOwnedLockFile(path, "owner", expected)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != "remove" || pathErr.Path != path || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("重试耗尽错误包装错误: %T %v", err, err)
		}
		if calls != fileLockRemoveMaxAttempts || waits != fileLockRemoveMaxAttempts-1 {
			t.Fatalf("重试次数没有明确上界: calls=%d waits=%d", calls, waits)
		}
	})
}

// TestFileOwnerReplacementDuringRetryIsPreserved 验证等待重试期间的新 owner 锁不会被旧 owner 删除。
func TestFileOwnerReplacementDuringRetryIsPreserved(t *testing.T) {
	_, directory := newTestFileDriver(t)
	path := filepath.Join(directory, "owner-replaced.lock")
	backup := filepath.Join(directory, "owner-replaced.lock.old")
	expected := writeWindowsLockFixture(t, path, "old-owner")
	originalDelete := windowsDeleteLockHandle
	originalSleep := windowsLockRetrySleep
	defer func() {
		windowsDeleteLockHandle = originalDelete
		windowsLockRetrySleep = originalSleep
	}()
	calls := 0
	windowsDeleteLockHandle = func(*os.File) error {
		calls++
		if err := os.Rename(path, backup); err != nil {
			t.Fatalf("移动旧锁失败: %v", err)
		}
		writeWindowsLockFixture(t, path, "new-owner")
		return windows.ERROR_SHARING_VIOLATION
	}
	windowsLockRetrySleep = func(time.Duration) {}
	deleted, err := removeOwnedLockFile(path, "old-owner", expected)
	if err != nil {
		t.Fatalf("锁被替换后应视为所有权丢失: %v", err)
	}
	if deleted || calls != 1 {
		t.Fatalf("旧 owner 不得继续删除新锁: deleted=%t calls=%d", deleted, calls)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取替换后的锁失败: %v", err)
	}
	var payload fileSessionLock
	if err = json.Unmarshal(data, &payload); err != nil || payload.Owner != "new-owner" {
		t.Fatalf("新 owner 锁被破坏: payload=%+v err=%v", payload, err)
	}
}

// TestFileLockReplacementAfterValidationIsPreserved 验证校验完成后的路径替换不会让旧 owner 删除新锁。
func TestFileLockReplacementAfterValidationIsPreserved(t *testing.T) {
	_, directory := newTestFileDriver(t)
	path := filepath.Join(directory, "replacement-window.lock")
	backup := filepath.Join(directory, "replacement-window.lock.old")
	expected := writeWindowsLockFixture(t, path, "old-owner")
	originalDelete := windowsDeleteLockHandle
	defer func() {
		windowsDeleteLockHandle = originalDelete
	}()
	windowsDeleteLockHandle = func(handle *os.File) error {
		if err := os.Rename(path, backup); err != nil {
			t.Fatalf("移动已校验旧锁失败: %v", err)
		}
		writeWindowsLockFixture(t, path, "new-owner")
		return originalDelete(handle)
	}

	deleted, err := removeOwnedLockFile(path, "old-owner", expected)
	if err != nil || !deleted {
		t.Fatalf("删除已校验旧锁失败: deleted=%t err=%v", deleted, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("校验后创建的新锁必须保留: %v", err)
	}
	var payload fileSessionLock
	if err = json.Unmarshal(data, &payload); err != nil || payload.Owner != "new-owner" {
		t.Fatalf("新 owner 锁被破坏: payload=%+v err=%v", payload, err)
	}
}

// TestCreatedLockBindsHandleIdentity 验证创建锁的失败清理与成功返回都绑定最初句柄身份。
func TestCreatedLockBindsHandleIdentity(t *testing.T) {
	_, directory := newTestFileDriver(t)
	t.Run("成功返回创建句柄身份", func(t *testing.T) {
		path := filepath.Join(directory, "create-confirmed.lock")
		handle, err := createLockFile(path)
		if err != nil {
			t.Fatalf("创建待初始化锁失败: %v", err)
		}
		expected, err := handle.Stat()
		if err != nil {
			_ = handle.Close()
			t.Fatalf("读取创建句柄身份失败: %v", err)
		}
		actual, err := initializeCreatedLockFile(path, "owner", handle, time.Now(), writeSessionData)
		if err != nil {
			t.Fatalf("初始化锁失败: %v", err)
		}
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(expected, actual) || !os.SameFile(actual, current) {
			t.Fatalf("返回身份未绑定创建句柄或当前路径: expected=%#v actual=%#v current=%#v err=%v", expected, actual, current, err)
		}
		assertWindowsLockOwner(t, path, "owner")
		if _, err = removeOwnedLockFile(path, "owner", actual); err != nil {
			t.Fatalf("清理已确认锁失败: %v", err)
		}
	})

	t.Run("写入失败清理自身锁", func(t *testing.T) {
		path := filepath.Join(directory, "create-cleanup.lock")
		handle, err := createLockFile(path)
		if err != nil {
			t.Fatalf("创建待初始化锁失败: %v", err)
		}
		writeErr := errors.New("injected lock write failure")
		_, err = initializeCreatedLockFile(path, "owner", handle, time.Now(), func(io.Writer, []byte) error {
			return writeErr
		})
		if !errors.Is(err, writeErr) {
			t.Fatalf("写入错误必须保留: %v", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("写入失败后自身残锁必须清理: %v", statErr)
		}
	})

	t.Run("owner 复核失败清理自身锁", func(t *testing.T) {
		path := filepath.Join(directory, "create-owner-mismatch.lock")
		handle, err := createLockFile(path)
		if err != nil {
			t.Fatalf("创建待初始化锁失败: %v", err)
		}
		_, err = initializeCreatedLockFile(path, "expected-owner", handle, time.Now(), func(writer io.Writer, _ []byte) error {
			payload, marshalErr := json.Marshal(fileSessionLock{
				Owner: "unexpected-owner", ExpireAt: time.Now().Add(time.Minute).UnixNano(),
			})
			if marshalErr != nil {
				return marshalErr
			}
			return writeSessionData(writer, payload)
		})
		if !errors.Is(err, ErrUnsafeSessionFile) {
			t.Fatalf("owner 复核失败必须拒绝锁: %v", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("owner 复核失败后自身残锁必须清理: %v", statErr)
		}
	})

	t.Run("写入失败不删除替换锁", func(t *testing.T) {
		path := filepath.Join(directory, "create-failure.lock")
		backup := filepath.Join(directory, "create-failure.lock.old")
		handle, err := createLockFile(path)
		if err != nil {
			t.Fatalf("创建待初始化锁失败: %v", err)
		}
		writeErr := errors.New("injected lock write failure")
		_, err = initializeCreatedLockFile(path, "old-owner", handle, time.Now(), func(io.Writer, []byte) error {
			if renameErr := os.Rename(path, backup); renameErr != nil {
				return renameErr
			}
			writeWindowsLockFixture(t, path, "new-owner")
			return writeErr
		})
		if !errors.Is(err, writeErr) {
			t.Fatalf("写入错误必须保留: %v", err)
		}
		assertWindowsLockOwner(t, path, "new-owner")
	})

	t.Run("成功前拒绝路径身份替换", func(t *testing.T) {
		path := filepath.Join(directory, "create-success.lock")
		backup := filepath.Join(directory, "create-success.lock.old")
		handle, err := createLockFile(path)
		if err != nil {
			t.Fatalf("创建待初始化锁失败: %v", err)
		}
		info, err := initializeCreatedLockFile(path, "old-owner", handle, time.Now(), func(writer io.Writer, data []byte) error {
			if writeErr := writeSessionData(writer, data); writeErr != nil {
				return writeErr
			}
			if renameErr := os.Rename(path, backup); renameErr != nil {
				return renameErr
			}
			writeWindowsLockFixture(t, path, "new-owner")
			return nil
		})
		if !errors.Is(err, ErrUnsafeSessionFile) || info != nil {
			t.Fatalf("路径身份替换后不得返回已获取锁: info=%#v err=%v", info, err)
		}
		assertWindowsLockOwner(t, path, "new-owner")
	})
}

// TestStableLockObservationClearsAccessDenied 验证拒绝访问后观察到稳定有效锁时，截止结果恢复为锁超时。
func TestStableLockObservationClearsAccessDenied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access-denied-then-stable.lock")
	remembered := error(&os.PathError{Op: "open", Path: path, Err: windows.ERROR_ACCESS_DENIED})
	remembered = fileSessionLockAccessDeniedAfterRead(remembered, true, nil)
	deadline := time.Unix(0, 0)
	if err := fileSessionLockDeadlineError(deadline, deadline, remembered); !errors.Is(err, ErrSessionLockTimeout) {
		t.Fatalf("稳定读取后截止必须返回锁超时，而不是陈旧拒绝访问: %v", err)
	}
}

// assertWindowsLockOwner 校验测试路径仍由指定 owner 持有。
func assertWindowsLockOwner(t *testing.T, path, expectedOwner string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取替换后的锁失败: %v", err)
	}
	var payload fileSessionLock
	if err = json.Unmarshal(data, &payload); err != nil || payload.Owner != expectedOwner {
		t.Fatalf("锁 owner 不匹配: expected=%q payload=%+v err=%v", expectedOwner, payload, err)
	}
}

// TestFileLockAccessDeniedClassificationAndDeadline 验证获取锁阶段仅分类 Windows 拒绝访问，并在截止时保留原始错误。
func TestFileLockAccessDeniedClassificationAndDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access-denied.lock")
	original := &os.PathError{Op: "open", Path: path, Err: windows.ERROR_ACCESS_DENIED}
	if !isLockFileAccessDenied(original) {
		t.Fatal("Windows ERROR_ACCESS_DENIED 应在获取锁阶段进入竞争分类")
	}
	if isLockFileCreateTransient(original) || isLockFileReadTransient(original) {
		t.Fatal("拒绝访问不得被归类为共享冲突或用于删除重试")
	}

	start := time.Unix(0, 0)
	deadline := start.Add(fileSessionLockWait)
	if err := fileSessionLockDeadlineError(start, deadline, original); err != nil {
		t.Fatalf("获取截止前不应返回错误: %v", err)
	}
	err := fileSessionLockDeadlineError(deadline, deadline, original)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "open" || pathErr.Path != path || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("获取截止时必须保留原始拒绝访问路径错误: %T %v", err, err)
	}
	if err = fileSessionLockDeadlineError(deadline, deadline, nil); !errors.Is(err, ErrSessionLockTimeout) {
		t.Fatalf("不存在拒绝访问时截止应返回锁超时: %v", err)
	}
}

// TestLockRemovalPathErrorNormalizesOpenError 验证删除阶段统一操作名并保留底层 errors.Is 原因。
func TestLockRemovalPathErrorNormalizesOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remove-open-error.lock")
	err := lockRemovalPathError(path, &os.PathError{
		Op: "open", Path: path, Err: windows.ERROR_ACCESS_DENIED,
	})
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "remove" || pathErr.Path != path {
		t.Fatalf("删除错误未统一操作与路径: %T %v", err, err)
	}
	if pathErr.Err != windows.ERROR_ACCESS_DENIED || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("删除错误未直接保留底层原因: %#v", pathErr)
	}
}
