//go:build !windows

package util

import "os"

// restrictPrivateFile 将私钥句柄权限限制为仅属主可读写。
func restrictPrivateFile(file *os.File) error {
	return file.Chmod(0o600)
}
