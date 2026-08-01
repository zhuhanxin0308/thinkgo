//go:build windows

package driver

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cacheDataWindowsShareMode = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

// removeFileIfSame 在同一个 Windows 句柄上完成身份复核和删除标记，避免检查后按路径删除。
func removeFileIfSame(path string, expected os.FileInfo) (bool, error) {
	handle, err := openCacheDataFileForRemoval(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer handle.Close()

	openedInfo, err := handle.Stat()
	if err != nil {
		return false, err
	}
	currentInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular() || !openedInfo.Mode().IsRegular() {
		return false, ErrUnsafeCacheEntry
	}
	if expected == nil || !os.SameFile(expected, openedInfo) || !os.SameFile(openedInfo, currentInfo) {
		return false, nil
	}

	deleteEnabled := byte(1)
	err = windows.SetFileInformationByHandle(
		windows.Handle(handle.Fd()),
		windows.FileDispositionInfo,
		&deleteEnabled,
		uint32(unsafe.Sizeof(deleteEnabled)),
	)
	runtime.KeepAlive(handle)
	if err != nil {
		return false, &os.PathError{Op: "remove", Path: path, Err: err}
	}
	return true, nil
}

func openCacheDataFileForRemoval(path string) (*os.File, error) {
	windowsPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		windowsPath,
		windows.GENERIC_READ|windows.DELETE,
		cacheDataWindowsShareMode,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
