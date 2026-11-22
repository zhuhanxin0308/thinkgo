//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package driver

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openLogDirectory(path string) (*os.File, error) {
	// #nosec G304 -- 最后一段只接受真实目录，锁定后还会复核路径身份。
	return os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

func removeObservedLogFile(candidate managedLogFile) (removed bool, result error) {
	// #nosec G304 -- 目录锁下已排除活动写者，拒绝链接并在删除前核验候选身份。
	file, err := os.OpenFile(candidate.path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	info, matches, err := inspectLogHandle(candidate.path, file)
	if err != nil {
		return false, err
	}
	if !matches || !os.SameFile(candidate.info, info) {
		return false, errUnsafeLogFilePath
	}
	err = os.Remove(candidate.path)
	return err == nil, err
}
