package business

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// readBoundedContractFile 仅在显式指定的配置目录内打开叶子文件，阻止符号链接逃逸。
func readBoundedContractFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("合同文件路径为空")
	}
	directory, name := filepath.Split(path)
	if name == "" || name == "." || name == ".." {
		return nil, errors.New("合同文件路径缺少合法文件名")
	}
	if directory == "" {
		directory = "."
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// 最多读取预算外一字节，保留调用方原有的“超预算”判定与错误语义。
	return io.ReadAll(io.LimitReader(file, maximumConfigBytes+1))
}
