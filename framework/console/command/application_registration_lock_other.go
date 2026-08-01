//go:build !windows

package command

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockApplicationRegistrationFile(file *os.File) error {
	if file == nil {
		return os.ErrInvalid
	}
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlockApplicationRegistrationFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func closeApplicationRegistrationFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
