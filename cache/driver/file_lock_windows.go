//go:build windows

package driver

import (
	"errors"
	"os"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/internal/winfile"
	"golang.org/x/sys/windows"
)

const (
	cacheLockRemoveMaxBackoff = 8 * time.Millisecond
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
	return winfile.Open(path, access, disposition)
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
	return winfile.Delete(handle)
}

func nextCacheLockRemoveBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > cacheLockRemoveMaxBackoff {
		return cacheLockRemoveMaxBackoff
	}
	return next
}
