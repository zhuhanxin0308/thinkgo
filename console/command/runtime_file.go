package command

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

// writeProjectFileAtomically 在项目根目录约束内原子替换单个文件，
// 配置缓存、路由缓存和供应商发布共享同一套路径与持久化保证。
func writeProjectFileAtomically(app *framework.App, target string, content []byte) (returnErr error) {
	if app == nil {
		return framework.ErrNilApplication
	}
	relative, err := projectRelativePath(app.GetRootPath(), target)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(app.GetRootPath())
	if err != nil {
		return fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if err := root.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}
	permission := os.FileMode(0o644)
	if information, statErr := root.Lstat(relative); statErr == nil {
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
			return fmt.Errorf("目标必须是普通文件: %s", target)
		}
		permission = information.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("检查目标文件失败: %w", statErr)
	}

	temporary := fmt.Sprintf("%s.tmp-%d", relative, time.Now().UnixNano())
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			removeErr := root.Remove(temporary)
			if !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, removeErr)
			}
		}
	}()
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(fmt.Errorf("写入临时文件失败: %w", writeErr), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("同步临时文件失败: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := root.Rename(temporary, relative); err != nil {
		return fmt.Errorf("原子替换目标文件失败: %w", err)
	}
	removeTemporary = false
	return nil
}

// projectRelativePath 将绝对或相对目标规范为项目根目录内的相对路径。
func projectRelativePath(basePath, target string) (string, error) {
	basePath = filepath.Clean(strings.TrimSpace(basePath))
	if basePath == "" || target == "" || strings.TrimSpace(target) != target {
		return "", fmt.Errorf("项目文件路径不能为空或包含首尾空白")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(basePath, target)
	}
	absoluteTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", fmt.Errorf("解析项目文件路径失败: %w", err)
	}
	absoluteBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("解析项目根目录失败: %w", err)
	}
	relative, err := filepath.Rel(absoluteBase, absoluteTarget)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("目标文件必须位于项目根目录内: %s", target)
	}
	return filepath.Clean(relative), nil
}

// safeOptionalDirectory 校验 optimize:* 的可选 dir 参数，保留 ThinkPHP
// 调用方式，同时禁止目录穿越和隐式绝对路径。
func safeOptionalDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if value == "." || value == ".." || filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.ContainsAny(value, `/\`) {
		return "", fmt.Errorf("dir 必须是单一安全目录名")
	}
	for _, character := range value {
		if character <= ' ' || character == 0x7f {
			return "", fmt.Errorf("dir 包含非法字符")
		}
	}
	return value, nil
}
