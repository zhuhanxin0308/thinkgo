//go:build (linux && !android) || (darwin && !ios)

package winfile

import (
	"errors"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

// Flock 在有效句柄的受保护生命周期内调用系统文件锁，避免关闭与描述符复用竞态。
func Flock(handle *os.File, operation int) error {
	if handle == nil {
		return os.ErrInvalid
	}
	connection, err := handle.SyscallConn()
	if err != nil {
		return err
	}
	var lockErr error
	controlErr := connection.Control(func(descriptor uintptr) {
		fd, err := unixFileDescriptor(descriptor)
		if err != nil {
			lockErr = err
			return
		}
		lockErr = unix.Flock(fd, operation)
	})
	return errors.Join(controlErr, lockErr)
}

// unixFileDescriptor 按 Linux 和 macOS 系统调用使用的有符号 32 位描述符检查上界。
func unixFileDescriptor(descriptor uintptr) (int, error) {
	if descriptor > math.MaxInt32 {
		return 0, os.ErrInvalid
	}
	return int(descriptor), nil
}
