//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris

package driver

import (
	"errors"
	"os"
)

// 当前平台没有实现可在进程退出时释放的目录锁，写入之前显式返回错误。
func openLogDirectory(string) (*os.File, error)          { return nil, errors.ErrUnsupported }
func removeObservedLogFile(managedLogFile) (bool, error) { return false, errors.ErrUnsupported }
