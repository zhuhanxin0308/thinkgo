//go:build windows

package driver

import (
	"errors"
	"os"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
)

// removeSessionFileIfSame 复用身份保护删除原语，同时保留会话错误契约。
func removeSessionFileIfSame(path string, expected os.FileInfo) (bool, error) {
	removed, err := winfile.RemoveIfSame(path, expected)
	if errors.Is(err, winfile.ErrUnsafeFile) {
		return false, ErrUnsafeSessionFile
	}
	return removed, err
}
