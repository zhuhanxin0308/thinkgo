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
	files, err := modzip.CheckDir(directory)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files.Valid))
	for _, path := range files.Valid {
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return "", err
		}
		paths = append(paths, filepath.ToSlash(relative))
	}
	root, err := os.OpenRoot(directory)
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
