//go:build windows

package driver

import (
	"errors"
	"os"

	"github.com/zhuhanxin0308/thinkgo/framework/internal/winfile"
)

// removeFileIfSame 复用身份保护删除原语，同时保留缓存错误契约。
func removeFileIfSame(path string, expected os.FileInfo) (bool, error) {
	removed, err := winfile.RemoveIfSame(path, expected)
	if errors.Is(err, winfile.ErrUnsafeFile) {
		return false, ErrUnsafeCacheEntry
	}
	return removed, err
}
