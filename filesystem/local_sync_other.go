//go:build !windows

package filesystem

import (
	"errors"
	"fmt"
	"os"
)

func syncLocalDirectory(root *os.Root, directory string) error {
	opened, err := root.Open(rootName(directory))
	if err != nil {
		return fmt.Errorf("打开待同步目录失败: %w", err)
	}
	syncErr := opened.Sync()
	closeErr := opened.Close()
	return errors.Join(
		wrapOperationError("同步目录句柄失败", syncErr),
		wrapOperationError("关闭目录句柄失败", closeErr),
	)
}
