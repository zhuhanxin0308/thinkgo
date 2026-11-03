//go:build unix

package filesystem

import (
	"os"
	"syscall"
)

func validateLocalDirectoryMode(mode os.FileMode) error {
	// Reject sticky shared directories as well: this API does not depend on
	// per-entry sticky-bit exceptions for publication and cleanup safety.
	if mode.Perm()&0o022 != 0 {
		return ErrUnsafeLocalWriteDirectory
	}
	return nil
}

func validateLocalDirectoryOwnership(information os.FileInfo) error {
	if err := validateLocalDirectoryMode(information.Mode()); err != nil {
		return err
	}
	stat, ok := information.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Uid != 0 && int64(stat.Uid) != int64(os.Geteuid()) {
		// A foreign owner can grant write permission later, even if the current
		// mode appears private. Root and the application UID are trusted actors.
		return ErrUnsafeLocalWriteDirectory
	}
	return nil
}
