package main

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"
)

// digestLocalModule 按 Go 模块归档的文件范围计算稳定摘要，排除 VCS 元数据及嵌套模块。
func digestLocalModule(directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", errors.New("模块清单缺少本地替换目录")
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	files, err := modzip.CheckDir(canonical)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files.Valid))
	hasGoMod := false
	for _, path := range files.Valid {
		if strings.EqualFold(filepath.Base(path), ".git") {
			// Git worktree 与子模块用普通文件保存工作树指针。
			continue
		}
		relative, err := filepath.Rel(canonical, path)
		if err != nil {
			return "", err
		}
		hasGoMod = hasGoMod || relative == "go.mod"
		paths = append(paths, filepath.ToSlash(relative))
	}
	if !hasGoMod {
		return "", errors.New("本地替换模块缺少 go.mod")
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return "", err
	}
	defer root.Close()
	sum, err := dirhash.Hash1(paths, func(name string) (io.ReadCloser, error) {
		info, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("本地模块文件已变更")
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		actual, err := file.Stat()
		if err != nil {
			return nil, errors.Join(err, file.Close())
		}
		if !os.SameFile(info, actual) {
			return nil, errors.Join(errors.New("本地模块文件已变更"), file.Close())
		}
		return file, nil
	})
	if err != nil {
		return "", err
	}
	digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sum, "h1:"))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest), nil
}
