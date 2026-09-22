//go:build !windows

package driver

import (
	"errors"
	"os"
)

// removeSessionFileIfSame 先绑定打开句柄再复核身份，尽量缩小普通 Unix 文件删除的竞态窗口。
func removeSessionFileIfSame(path string, expected os.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() {
		return false, ErrUnsafeSessionFile
	}
	// #nosec G304 -- 调用方仅传入会话根目录内的受管文件；此处已拒绝符号链接，删除前继续核对句柄与路径的 SameFile 身份。
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
		return false, ErrUnsafeSessionFile
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
