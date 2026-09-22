//go:build !windows

package driver

import "os"

// restrictLogPath 将日志目录限制为 0700、日志文件限制为 0600。
func restrictLogPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
