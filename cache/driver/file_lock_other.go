//go:build (linux && !android) || (darwin && !ios)

package driver

import (
	"errors"
	"os"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
	"golang.org/x/sys/unix"
)

// createCacheLockFile 使用独占创建语义建立缓存锁文件。
func createCacheLockFile(path string) (*os.File, error) {
	// #nosec G304 -- AcquireLock 仅传入已规范化缓存根目录下的哈希锁名，O_EXCL 拒绝覆盖已有文件及符号链接。
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

// openCacheLockFileForUpdate 以读写模式打开租约文件，续租时不会再通过路径替换文件身份。
func openCacheLockFileForUpdate(path string) (*os.File, error) {
	// #nosec G304 -- openLockedCacheLockFile 使用缓存根目录下的哈希锁名，打开前后复核普通文件、符号链接和 SameFile 身份。
	return os.OpenFile(path, os.O_RDWR, 0o600)
}

// lockCacheLockFile 使用 Unix 文件锁串行化跨进程的读取、删除和续租。
func lockCacheLockFile(handle *os.File) error {
	return winfile.Flock(handle, unix.LOCK_EX)
}

// unlockCacheLockFile 释放由 lockCacheLockFile 建立的 Unix 文件锁。
func unlockCacheLockFile(handle *os.File) error {
	return winfile.Flock(handle, unix.LOCK_UN)
}

// discardCreatedCacheLockFile 仅在无法取得文件身份时关闭句柄，避免按路径误删替换后的文件。
func discardCreatedCacheLockFile(_ string, handle *os.File, expected os.FileInfo) error {
	if handle == nil {
		return nil
	}
	if expected == nil {
		return handle.Close()
	}
	opened, statErr := handle.Stat()
	if statErr != nil || !os.SameFile(expected, opened) {
		return errors.Join(statErr, handle.Close())
	}
	_, removeErr := removeLockedCacheLockFile(handle.Name(), handle, expected)
	closeErr := handle.Close()
	return errors.Join(removeErr, closeErr)
}

// removeLockedCacheLockFile 只能在已持有句柄文件锁时调用，避免检查身份后被协作进程替换。
func removeLockedCacheLockFile(path string, handle *os.File, expected os.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if expected == nil || !os.SameFile(expected, current) {
		return false, nil
	}
	if err = os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
