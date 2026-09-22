//go:build !windows

package driver

import "os"

func openSessionFile(path string) (*os.File, error) { return os.Open(path) }

// replaceSessionFile 在类 Unix 文件系统上使用同目录 rename 原子替换 Session 文件。
func replaceSessionFile(source, target string) error {
	return os.Rename(source, target)
}
