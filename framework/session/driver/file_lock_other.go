//go:build !windows

package driver

import (
	"errors"
	"os"
)

// createLockFile 使用类 Unix 的独占创建快路径建立锁文件。
func createLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

// openLockFile 使用类 Unix 的普通只读快路径打开锁文件。
func openLockFile(path string) (*os.File, error) {
	return os.Open(path)
}

func isLockFileCreateTransient(error) bool { return false }

func isLockFileReadTransient(error) bool { return false }

func isLockFileAccessDenied(error) bool { return false }

// discardUnidentifiedCreatedLockFile 无法确认身份时只关闭句柄，避免按可替换路径误删其他文件。
func discardUnidentifiedCreatedLockFile(_ string, handle *os.File) error {
	return handle.Close()
}

// removeOwnedLockFile 在类 Unix 上复核身份与 owner 后直接删除，不增加重试。
func removeOwnedLockFile(path, owner string, expected os.FileInfo) (bool, error) {
	owned, err := lockFileMatchesOwner(path, owner, expected)
	if err != nil || !owned {
		return false, err
	}
	if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
