package driver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File 文件会话驱动。
type File struct {
	path string
}

// NewFile 创建文件会话驱动。
func NewFile(path string) *File {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = os.MkdirAll(path, 0o755)
	}
	return &File{path: path}
}

// Read 读取会话内容。
func (f *File) Read(id string) (string, error) {
	file, err := f.resolveFilePath(id)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return "", nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Write 写入会话内容。
func (f *File) Write(id string, data string) error {
	file, err := f.resolveFilePath(id)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(data), 0o644)
}

// Delete 删除会话内容。
func (f *File) Delete(id string) error {
	file, err := f.resolveFilePath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Clear 文件驱动不执行批量删除，避免误删目录内其他内容。
func (f *File) Clear() error {
	return nil
}

// GC 回收超过 maxLifetime 未更新的会话文件，避免过期会话在磁盘无限堆积。
// 以文件最后修改时间作为判断依据；maxLifetime<=0 时不执行回收。
// 返回成功删除的文件数量。
func (f *File) GC(maxLifetime time.Duration) (int, error) {
	if maxLifetime <= 0 {
		return 0, nil
	}

	entries, err := os.ReadDir(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	deadline := time.Now().Add(-maxLifetime)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !isSafeFileSessionID(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(deadline) {
			if err := os.Remove(filepath.Join(f.path, entry.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// resolveFilePath 校验 Session ID 并确保最终路径不会逃逸存储目录。
func (f *File) resolveFilePath(id string) (string, error) {
	if !isSafeFileSessionID(id) {
		return "", errors.New("invalid session id")
	}

	basePath, err := filepath.Abs(f.path)
	if err != nil {
		return "", err
	}
	targetPath, err := filepath.Abs(filepath.Join(f.path, id))
	if err != nil {
		return "", err
	}

	if targetPath != basePath && !strings.HasPrefix(targetPath, basePath+string(os.PathSeparator)) {
		return "", errors.New("session path escapes base directory")
	}
	return targetPath, nil
}

// isSafeFileSessionID 限制文件型 Session ID 的字符集，避免被解释为路径片段。
func isSafeFileSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}

	for _, char := range id {
		isDigit := char >= '0' && char <= '9'
		isLower := char >= 'a' && char <= 'z'
		isUpper := char >= 'A' && char <= 'Z'
		if isDigit || isLower || isUpper || char == '-' || char == '_' {
			continue
		}
		return false
	}

	return true
}
