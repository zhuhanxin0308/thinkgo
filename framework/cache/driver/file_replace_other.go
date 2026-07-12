//go:build !windows

package driver

import "os"

// replaceCacheFile 在类 Unix 文件系统上使用同目录 rename 原子替换缓存文件。
func replaceCacheFile(source, target string) error {
	return os.Rename(source, target)
}
