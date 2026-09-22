//go:build !windows

package driver

import "os"

func openSessionFile(path string) (*os.File, error) {
	// #nosec G304 -- 路径来自会话根目录内的哈希文件名；readManagedFileWithOpen 在读取前完成 Lstat、普通文件和 SameFile 复核。
	return os.Open(path)
}

// replaceSessionFile 在类 Unix 文件系统上使用同目录 rename 原子替换 Session 文件。
func replaceSessionFile(source, target string) error {
	return os.Rename(source, target)
}
