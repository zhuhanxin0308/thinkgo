//go:build !windows && (!linux || android) && (!darwin || ios)

package winfile

import "os"

func openGuard(string) (*os.File, error)  { return nil, os.ErrInvalid }
func tryGuardLock(*os.File) (bool, error) { return false, os.ErrInvalid }
func unlockGuard(*os.File) error          { return os.ErrInvalid }
