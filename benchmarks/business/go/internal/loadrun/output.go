package loadrun

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SaveReport 先完整写入同目录临时文件，再以硬链接原子发布；已有结果永不覆盖。
// .gz 后缀写入 gzip，其他后缀写入 JSON；输出目录须由实验编排器事先创建。
func SaveReport(path string, report Report) (returnErr error) {
	if strings.TrimSpace(path) == "" {
		return errors.New("结果输出路径不能为空")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".load-report-*.tmp")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, os.Remove(file.Name())) }()
	defer file.Close()
	var writer io.Writer = file
	var compressed *gzip.Writer
	if strings.HasSuffix(path, ".gz") {
		compressed = gzip.NewWriter(file)
		writer = compressed
	}
	encodeErr := json.NewEncoder(writer).Encode(report)
	if compressed != nil {
		encodeErr = errors.Join(encodeErr, compressed.Close())
	}
	if encodeErr != nil {
		return encodeErr
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Link(file.Name(), path)
}
