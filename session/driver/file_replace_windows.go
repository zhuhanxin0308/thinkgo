//go:build windows

package driver

import (
	"os"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
	"golang.org/x/sys/windows"
)

func openSessionFile(path string) (*os.File, error) {
	return winfile.Open(path, windows.GENERIC_READ, windows.OPEN_EXISTING)
}

// replaceSessionFile 在 Windows 上使用覆盖语义原子替换受管 Session 文件。
func replaceSessionFile(source, target string) error {
	return winfile.Replace(source, target)
}
