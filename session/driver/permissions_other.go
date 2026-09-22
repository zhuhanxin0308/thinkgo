//go:build !windows

package driver

import "os"

// restrictSessionPath 将 Session 目录限制为 0700、文件限制为 0600。
func restrictSessionPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
