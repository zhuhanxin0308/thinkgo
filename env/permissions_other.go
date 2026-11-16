//go:build !windows

package env

import "os"

func verifyEnvFilePermissions(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return ErrEnvPermissionsTooOpen
	}
	return nil
}
