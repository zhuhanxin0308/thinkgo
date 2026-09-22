//go:build (linux && !android) || (darwin && !ios)

package winfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openGuard(path string) (*os.File, error) {
	// #nosec G304 -- 调用方用受管根目录和固定分片名构造路径；O_NOFOLLOW 拒绝符号链接，WithGuard 在加锁后复核普通文件与身份。
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
}

func tryGuardLock(handle *os.File) (bool, error) {
	err := Flock(handle, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockGuard(handle *os.File) error {
	return Flock(handle, unix.LOCK_UN)
}
