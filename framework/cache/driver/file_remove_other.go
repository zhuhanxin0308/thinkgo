//go:build !windows

package driver

import (
	"errors"
	"os"
)

// removeFileIfSame 先绑定打开句柄再复核身份，尽量缩小普通 Unix 文件删除的竞态窗口。
func removeFileIfSame(path string, expected os.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() {
		return false, ErrUnsafeCacheEntry
	}
	handle, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	opened, statErr := handle.Stat()
	current, verifyErr := os.Lstat(path)
	if statErr != nil || verifyErr != nil {
		_ = handle.Close()
		return false, errors.Join(statErr, verifyErr)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !opened.Mode().IsRegular() {
		_ = handle.Close()
		return false, ErrUnsafeCacheEntry
	}
	if expected == nil || !os.SameFile(expected, opened) || !os.SameFile(opened, current) {
		_ = handle.Close()
		return false, nil
	}
	removeErr := os.Remove(path)
	closeErr := handle.Close()
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return removeErr == nil, errors.Join(removeErr, closeErr)
}
