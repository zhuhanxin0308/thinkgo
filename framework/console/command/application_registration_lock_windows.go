//go:build windows

package command

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockApplicationRegistrationFile(file *os.File) error {
	if file == nil {
		return os.ErrInvalid
	}
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}

func unlockApplicationRegistrationFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}

func closeApplicationRegistrationFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
