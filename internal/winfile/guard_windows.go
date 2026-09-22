//go:build windows

package winfile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openGuard(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// 稳定锁文件不参与删除共享，持有或等待锁期间禁止替换锁的身份。
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func tryGuardLock(handle *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(handle.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func unlockGuard(handle *os.File) error {
	return windows.UnlockFileEx(windows.Handle(handle.Fd()), 0, 1, 0, &windows.Overlapped{})
}
