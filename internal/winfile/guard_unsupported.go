//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris

package winfile

import (
	"errors"
	"os"
)

func openGuard(string) (*os.File, error)        { return nil, os.ErrInvalid }
func tryGuardLock(*os.File) (bool, error)       { return false, os.ErrInvalid }
func unlockGuard(*os.File) error                { return os.ErrInvalid }
func trySharedGuardLock(*os.File) (bool, error) { return false, errors.ErrUnsupported }
