package command

import (
	"errors"
	"path/filepath"
)

// resolveApplicationSourceDirectory 将模块根和 Go 返回的源码目录统一为真实绝对路径。
// macOS 的 /var 与 /private/var 等目录别名不能直接用于模块归属比较。
func resolveApplicationSourceDirectory(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

// relativeApplicationSourceDirectory 使用已解析的模块根检查源码归属，同时拒绝链接到模块外的目录。
func relativeApplicationSourceDirectory(base, directory string) (string, error) {
	resolved, err := resolveApplicationSourceDirectory(directory)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, resolved)
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(relative) {
		return "", errors.New("源码目录位于模块根目录之外")
	}
	return relative, nil
}
