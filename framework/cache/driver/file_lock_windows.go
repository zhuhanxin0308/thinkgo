//go:build windows

package driver

import (
	"errors"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cacheLockWindowsShareMode     = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	cacheLockWindowsDeleteEnabled = byte(1)
	cacheLockRemoveMaxBackoff     = 8 * time.Millisecond
)

// createCacheLockFile 使用 Windows CREATE_NEW 和共享删除语义建立缓存锁文件。
func createCacheLockFile(path string) (*os.File, error) {
	return createWindowsCacheLockFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.CREATE_NEW)
}

// openCacheLockFileForUpdate 以读写模式打开租约文件，续租时保持同一文件身份。
func openCacheLockFileForUpdate(path string) (*os.File, error) {
	return createWindowsCacheLockFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.OPEN_EXISTING)
}

// lockCacheLockFile 使用 Windows 文件范围锁串行化跨进程的租约读取、删除和续期。
func lockCacheLockFile(handle *os.File) error {
	return windows.LockFileEx(
		windows.Handle(handle.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}

// unlockCacheLockFile 释放由 lockCacheLockFile 建立的 Windows 文件锁。
func unlockCacheLockFile(handle *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(handle.Fd()),
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}

func createWindowsCacheLockFile(path string, access, disposition uint32) (*os.File, error) {
	windowsPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, cacheLockPathError("open", path, err)
	}
	handle, err := windows.CreateFile(
		windowsPath,
		access,
		cacheLockWindowsShareMode,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, cacheLockPathError("open", path, err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

// discardCreatedCacheLockFile 在创建句柄上标记删除，避免关闭句柄后再按路径删除。
func discardCreatedCacheLockFile(_ string, handle *os.File, expected os.FileInfo) error {
	if expected == nil {
		return handle.Close()
	}
	opened, err := handle.Stat()
	if err != nil {
		return errors.Join(err, handle.Close())
	}
	if !os.SameFile(expected, opened) {
		return handle.Close()
	}
	return errors.Join(deleteWindowsCacheLockHandle(handle), handle.Close())
}

func deleteWindowsCacheLockHandle(handle *os.File) error {
	deleteEnabled := cacheLockWindowsDeleteEnabled
	err := windows.SetFileInformationByHandle(
		windows.Handle(handle.Fd()),
		windows.FileDispositionInfo,
		&deleteEnabled,
		uint32(unsafe.Sizeof(deleteEnabled)),
	)
	runtime.KeepAlive(handle)
	return err
}

func nextCacheLockRemoveBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > cacheLockRemoveMaxBackoff {
		return cacheLockRemoveMaxBackoff
	}
	return next
}

func cacheLockPathError(operation, path string, err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &os.PathError{Op: operation, Path: path, Err: err}
}
