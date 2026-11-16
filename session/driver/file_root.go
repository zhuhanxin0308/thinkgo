package driver

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/apppath"
)

// NewFileInRoot 在指定运行目录内创建文件 Session，拒绝指向目录外的相对路径。
func NewFileInRoot(rootPath, relativePath string) (*File, error) {
	if !filepath.IsLocal(relativePath) || containsControlCharacter(relativePath) {
		return nil, fmt.Errorf("%w: 相对路径 %q 非法", ErrInvalidSessionPath, relativePath)
	}
	store, err := openFileSessionInRoot(rootPath, relativePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSessionPath, err)
	}
	return store, nil
}

func openFileSessionInRoot(rootPath, relativePath string) (*File, error) {
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	actualRoot, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	if _, err = verifyFileSessionDirectory(root, actualRoot); err != nil {
		return nil, err
	}

	// 允许指向运行目录内部的绝对链接，再用已打开的根目录限制创建和访问。
	target, err := apppath.ResolveExisting(filepath.Join(actualRoot, relativePath))
	if err != nil {
		return nil, err
	}
	subpath, err := filepath.Rel(actualRoot, target)
	if err != nil || !filepath.IsLocal(subpath) {
		return nil, fmt.Errorf("文件会话目录必须位于应用运行目录内: %q", relativePath)
	}
	if err = root.MkdirAll(subpath, 0o700); err != nil {
		return nil, err
	}
	directory, err := root.OpenRoot(subpath)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	info, err := verifyFileSessionDirectory(directory, target)
	if err != nil {
		return nil, err
	}
	if err = restrictFileSessionDirectory(directory, target, info); err != nil {
		return nil, fmt.Errorf("限制目录权限失败: %w", err)
	}
	if _, err = verifyFileSessionDirectory(directory, target); err != nil {
		return nil, err
	}
	return &File{path: target}, nil
}

// verifyFileSessionDirectory 确认保存的路径与当前打开的目录相同，且解析后仍是原路径。
func verifyFileSessionDirectory(directory *os.Root, path string) (os.FileInfo, error) {
	expected, err := directory.Stat(".")
	if err != nil {
		return nil, err
	}
	actual, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(path, resolved)
	if err != nil || relative != "." || !actual.IsDir() || !os.SameFile(expected, actual) {
		return nil, fmt.Errorf("文件会话目录已变更: %q", path)
	}
	return expected, nil
}
