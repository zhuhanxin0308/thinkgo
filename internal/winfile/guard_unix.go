//go:build (linux && !android) || (darwin && !ios)

package winfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openGuard(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
}

func tryGuardLock(handle *os.File) (bool, error) {
	err := unix.Flock(int(handle.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockGuard(handle *os.File) error {
	return unix.Flock(int(handle.Fd()), unix.LOCK_UN)
}
