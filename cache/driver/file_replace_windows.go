//go:build windows

package driver

import (
	"os"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
	"golang.org/x/sys/windows"
)

func openCacheDataFile(path string) (*os.File, error) {
	return winfile.Open(path, windows.GENERIC_READ, windows.OPEN_EXISTING)
}

// replaceCacheFile 在 Windows 上使用覆盖语义原子替换已有缓存文件。
func replaceCacheFile(source, target string) error {
	return winfile.Replace(source, target)
}
