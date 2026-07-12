//go:build windows

package driver

import "golang.org/x/sys/windows"

// replaceCacheFile 在 Windows 上使用覆盖语义原子替换已有缓存文件。
func replaceCacheFile(source, target string) error {
	return windows.Rename(source, target)
}
