package framework

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// normalizeAppStoragePath 统一解析缓存、Session 等本地存储路径。
// 绝对路径按显式配置使用；相对路径必须位于应用根目录内。
func normalizeAppStoragePath(basePath, configuredPath string) (string, error) {
	if configuredPath == "" || strings.TrimSpace(configuredPath) != configuredPath ||
		!utf8.ValidString(configuredPath) || containsAppStoragePathControl(configuredPath) {
		return "", errors.New("path 必须是不含首尾空白和控制字符的有效字符串")
	}
	cleaned := filepath.Clean(configuredPath)
	if filepath.IsAbs(cleaned) {
		return cleaned, nil
	}
	root, err := filepath.Abs(basePath)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.Abs(filepath.Join(root, cleaned))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("相对路径不能离开应用根目录")
	}
	return filepath.Clean(resolved), nil
}

func containsAppStoragePathControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
