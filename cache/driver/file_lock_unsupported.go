//go:build (!windows && !linux && !darwin) || android || ios

package driver

import (
	"errors"
	"fmt"
	"os"
)

var errCacheFileLockUnsupported = fmt.Errorf("当前平台不支持缓存文件锁: %w", errors.ErrUnsupported)

// createCacheLockFile 在未承诺支持的平台上明确拒绝文件锁，避免退化成无跨进程互斥的实现。
func createCacheLockFile(string) (*os.File, error) {
	return nil, errCacheFileLockUnsupported
}

// openCacheLockFileForUpdate 在未承诺支持的平台上明确拒绝更新文件锁。
func openCacheLockFileForUpdate(string) (*os.File, error) {
	return nil, errCacheFileLockUnsupported
}

// lockCacheLockFile 不得在未承诺支持的平台上伪造加锁成功。
func lockCacheLockFile(*os.File) error {
	return errCacheFileLockUnsupported
}

// unlockCacheLockFile 与加锁能力保持相同的不支持语义。
func unlockCacheLockFile(*os.File) error {
	return errCacheFileLockUnsupported
}

// discardCreatedCacheLockFile 关闭意外传入的句柄，并保留平台不支持错误。
func discardCreatedCacheLockFile(_ string, handle *os.File, _ os.FileInfo) error {
	if handle == nil {
		return errCacheFileLockUnsupported
	}
	return errors.Join(errCacheFileLockUnsupported, handle.Close())
}

// removeLockedCacheLockFile 不得在未承诺支持的平台上执行缺少锁保护的删除。
func removeLockedCacheLockFile(string, *os.File, os.FileInfo) (bool, error) {
	return false, errCacheFileLockUnsupported
}
