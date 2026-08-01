//go:build windows

package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fileLockRemoveMaxAttempts    = 6
	fileLockRemoveInitialBackoff = time.Millisecond
	fileLockRemoveMaxBackoff     = 8 * time.Millisecond
	fileLockWindowsShareMode     = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	fileLockWindowsDeleteEnabled = byte(1)
)

var (
	windowsDeleteLockHandle = deleteWindowsLockHandle
	windowsLockRetrySleep   = time.Sleep
)

// createLockFile 使用 Windows CREATE_NEW 语义并允许其他句柄读、写和删除共享。
func createLockFile(path string) (*os.File, error) {
	return createWindowsLockFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.CREATE_NEW)
}

// openLockFile 使用 Windows OPEN_EXISTING 语义并允许其他句柄读、写和删除共享。
func openLockFile(path string) (*os.File, error) {
	return createWindowsLockFile(path, windows.GENERIC_READ, windows.OPEN_EXISTING)
}

// openLockFileForRemoval 以读取和删除权限打开锁，并继续允许其他受管句柄共享删除。
func openLockFileForRemoval(path string) (*os.File, error) {
	return createWindowsLockFile(path, windows.GENERIC_READ|windows.DELETE, windows.OPEN_EXISTING)
}

// isLockFileCreateTransient 仅将可明确识别的 Windows 共享冲突视为创建锁的短暂竞争。
func isLockFileCreateTransient(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// isLockFileReadTransient 仅将 Windows 可明确识别的共享冲突视为读取锁时的短暂竞争。
func isLockFileReadTransient(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// isLockFileAccessDenied 标识需要由调用方以身份宽限窗口进一步区分的 Windows 拒绝访问。
func isLockFileAccessDenied(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

func createWindowsLockFile(path string, access, disposition uint32) (*os.File, error) {
	windowsPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, lockPathError("open", path, err)
	}
	handle, err := windows.CreateFile(
		windowsPath,
		access,
		fileLockWindowsShareMode,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, lockPathError("open", path, err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

// removeOwnedLockFile 使用同一句柄核验身份与 owner 并设置删除标记，只重试共享冲突。
func removeOwnedLockFile(path, owner string, expected os.FileInfo) (bool, error) {
	backoff := fileLockRemoveInitialBackoff
	for attempt := 0; attempt < fileLockRemoveMaxAttempts; attempt++ {
		handle, err := openLockFileForRemoval(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				return false, lockRemovalPathError(path, err)
			}
			if attempt == fileLockRemoveMaxAttempts-1 {
				return false, lockRemovalPathError(path, err)
			}
			windowsLockRetrySleep(backoff)
			backoff = nextLockRemovalBackoff(backoff)
			continue
		}
		owned, matchErr := windowsLockHandleMatchesOwner(path, handle, owner, expected)
		if matchErr != nil || !owned {
			_ = handle.Close()
			return false, matchErr
		}
		err = windowsDeleteLockHandle(handle)
		closeErr := handle.Close()
		err = errors.Join(err, closeErr)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				return false, lockRemovalPathError(path, err)
			}
			if attempt == fileLockRemoveMaxAttempts-1 {
				return false, lockRemovalPathError(path, err)
			}
			windowsLockRetrySleep(backoff)
			backoff = nextLockRemovalBackoff(backoff)
			continue
		}
		return true, nil
	}
	return false, nil
}

// discardUnidentifiedCreatedLockFile 在句柄身份无法读取时，直接在创建句柄上设置删除标记。
func discardUnidentifiedCreatedLockFile(_ string, handle *os.File) error {
	deleteErr := deleteWindowsLockHandle(handle)
	return errors.Join(deleteErr, handle.Close())
}

// windowsLockHandleMatchesOwner 从待删除句柄读取 owner，并确认句柄、期望身份与当前路径仍指向同一普通文件。
func windowsLockHandleMatchesOwner(path string, handle *os.File, owner string, expected os.FileInfo) (bool, error) {
	openedInfo, err := handle.Stat()
	if err != nil {
		return false, err
	}
	if !openedInfo.Mode().IsRegular() {
		return false, ErrUnsafeSessionFile
	}
	currentInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular() {
		return false, ErrUnsafeSessionFile
	}
	if expected == nil || !os.SameFile(expected, openedInfo) || !os.SameFile(openedInfo, currentInfo) {
		return false, nil
	}
	if owner == "" {
		return true, nil
	}
	if openedInfo.Size() < 0 || openedInfo.Size() > maxFileSessionLockBytes {
		return false, fmt.Errorf("%w: %d", ErrSessionEntryTooLarge, openedInfo.Size())
	}
	if _, err = handle.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(handle, maxFileSessionLockBytes+1))
	if err != nil {
		return false, err
	}
	if len(data) > maxFileSessionLockBytes {
		return false, ErrSessionEntryTooLarge
	}
	var payload fileSessionLock
	if err = json.Unmarshal(data, &payload); err != nil {
		return false, err
	}
	return payload.Owner == owner, nil
}

// deleteWindowsLockHandle 在已经完成身份和 owner 校验的同一句柄上设置删除标记。
func deleteWindowsLockHandle(handle *os.File) error {
	deleteEnabled := fileLockWindowsDeleteEnabled
	err := windows.SetFileInformationByHandle(
		windows.Handle(handle.Fd()),
		windows.FileDispositionInfo,
		&deleteEnabled,
		uint32(unsafe.Sizeof(deleteEnabled)),
	)
	runtime.KeepAlive(handle)
	return err
}

func nextLockRemovalBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > fileLockRemoveMaxBackoff {
		return fileLockRemoveMaxBackoff
	}
	return next
}

func lockRemovalPathError(path string, err error) error {
	if pathErr, ok := err.(*os.PathError); ok {
		err = pathErr.Err
	}
	return &os.PathError{Op: "remove", Path: path, Err: err}
}

func lockPathError(operation, path string, err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &os.PathError{Op: operation, Path: path, Err: err}
}
