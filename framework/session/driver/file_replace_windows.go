//go:build windows

package driver

import "golang.org/x/sys/windows"

// replaceSessionFile 在 Windows 上使用覆盖语义原子替换受管 Session 文件。
func replaceSessionFile(source, target string) error {
	return windows.Rename(source, target)
}
