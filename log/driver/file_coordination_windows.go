//go:build windows

package driver

import (
	"errors"
	"os"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
	"golang.org/x/sys/windows"
)

func openLogDirectory(path string) (*os.File, error) {
	// #nosec G304 -- 调用方验证目录身份，持有目录句柄期间禁止删除共享。
	return os.Open(path)
}

func removeObservedLogFile(candidate managedLogFile) (bool, error) {
	removed, err := winfile.RemoveIfSame(candidate.path, candidate.info)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return false, nil
	}
	if err == nil && !removed {
		return false, errUnsafeLogFilePath
	}
	return removed, err
}
