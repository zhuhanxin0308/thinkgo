// Package apppath resolves configured filesystem paths from the application root.
package apppath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Resolve preserves absolute paths and anchors relative paths to basePath.
// The application constructor makes basePath absolute before configuration is read.
func Resolve(basePath, configured string) string {
	configured = strings.TrimSpace(configured)
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(basePath, configured)
}

// ResolveExisting 解析最后一个已存在组件的真实路径，并拼回尚不存在的尾部。
func ResolveExisting(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var missing []string
	for {
		_, err = os.Lstat(current)
		if err == nil {
			realPath, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				realPath = filepath.Join(realPath, missing[index])
			}
			return realPath, nil
		}
		parent := filepath.Dir(current)
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if parent == current {
			return absolute, nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
