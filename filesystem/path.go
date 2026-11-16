package filesystem

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

var (
	// ErrInvalidPath 表示业务路径包含越界父目录或 Unicode 控制字符。
	ErrInvalidPath = errors.New("文件系统路径无效")
	// ErrSymbolicLink 对应 LocalFilesystemAdapter 默认的 DISALLOW_LINKS。
	ErrSymbolicLink = errors.New("文件系统不允许符号链接")
	// ErrURLNotSupported 表示当前磁盘没有配置 URL 前缀。
	ErrURLNotSupported = errors.New("当前文件系统驱动不支持 URL")
)

// normalizePath 对齐 Flysystem WhitespacePathNormalizer：反斜杠转为斜杠，
// 忽略空段和当前目录段，拒绝越界父目录与 Windows 文件名别名。
func normalizePath(path string) (string, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	for _, character := range path {
		if isUnicodeControl(character) {
			return "", fmt.Errorf("%w: 路径包含控制字符", ErrInvalidPath)
		}
	}
	parts := make([]string, 0, strings.Count(path, "/")+1)
	for _, part := range strings.Split(path, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return "", fmt.Errorf("%w: 路径越过磁盘根目录", ErrInvalidPath)
			}
			parts = parts[:len(parts)-1]
		default:
			if runtime.GOOS == "windows" && !validWindowsPathSegment(part) {
				return "", fmt.Errorf("%w: Windows 路径段 %q 无效", ErrInvalidPath, part)
			}
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/"), nil
}

func validWindowsPathSegment(part string) bool {
	if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || !filepath.IsLocal(part) {
		return false
	}
	// 设备名带扩展名时在不同 Windows 版本上的行为不同，统一检查名称主体。
	stem, _, _ := strings.Cut(part, ".")
	stem = strings.TrimRight(stem, " ")
	return stem == "" || filepath.IsLocal(stem)
}

func normalizeFilePath(path string) (string, error) {
	normalized, err := normalizePath(path)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", fmt.Errorf("%w: 文件路径不能为空", ErrInvalidPath)
	}
	return normalized, nil
}

func isUnicodeControl(character rune) bool {
	return unicode.IsControl(character) ||
		unicode.Is(unicode.Cf, character) ||
		unicode.Is(unicode.Co, character) ||
		unicode.Is(unicode.Cs, character)
}
