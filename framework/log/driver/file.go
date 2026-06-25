package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"thinkgo/framework/log"
)

// File 文件日志驱动。
// 支持按日期分文件、按大小自动轮转。
// 仅保留当前目标文件的单个句柄：目标文件名变化（跨天/轮转）时关闭旧句柄，
// 避免长跑进程随天数累积泄漏文件句柄。
type File struct {
	path        string
	maxFileSize int64
	mu          sync.Mutex
	currentName string
	currentFile *os.File
}

// NewFile 创建文件日志驱动。
func NewFile(path string, maxFileSize ...int64) *File {
	var maxSize int64
	if len(maxFileSize) > 0 {
		maxSize = maxFileSize[0]
	}
	driver := &File{
		path:        path,
		maxFileSize: maxSize,
	}
	_ = driver.ensureDir()
	return driver
}

// WriteEntry 立即写入单条日志。
func (d *File) WriteEntry(entry *log.LogEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureDir(); err != nil {
		return err
	}

	filename := d.getLogFile(entry.Time)
	file, err := d.getOrCreateFileLocked(filename)
	if err != nil {
		return err
	}

	if _, err = file.WriteString(entry.FormatEntry() + "\n"); err != nil {
		_ = d.closeFileLocked(filename)
		return err
	}
	return nil
}

// SaveEntries 批量保存日志条目。
func (d *File) SaveEntries(entries []*log.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureDir(); err != nil {
		return err
	}

	groups := make(map[string][]*log.LogEntry)
	for _, entry := range entries {
		date := entry.Time.Format("2006-01-02")
		groups[date] = append(groups[date], entry)
	}

	for _, dateEntries := range groups {
		filename := d.getLogFile(dateEntries[0].Time)
		file, err := d.getOrCreateFileLocked(filename)
		if err != nil {
			return err
		}

		for _, entry := range dateEntries {
			if _, err = file.WriteString(entry.FormatEntry() + "\n"); err != nil {
				_ = d.closeFileLocked(filename)
				return err
			}
		}
	}

	return nil
}

// Close 关闭驱动。
func (d *File) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closeCurrentLocked()
}

// getOrCreateFileLocked 获取当前目标日志文件句柄。
// 目标文件名与已打开句柄不同（跨天/轮转）时，先关闭旧句柄再打开新文件。
// 调用方必须先持有 d.mu。
func (d *File) getOrCreateFileLocked(filename string) (*os.File, error) {
	if d.currentFile != nil && d.currentName == filename {
		return d.currentFile, nil
	}

	// 目标文件变化，关闭上一个句柄，避免句柄随天数累积泄漏。
	_ = d.closeCurrentLocked()

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	d.currentName = filename
	d.currentFile = file
	return file, nil
}

// closeFileLocked 关闭指定文件句柄（若为当前句柄）。
// 调用方必须先持有 d.mu。
func (d *File) closeFileLocked(filename string) error {
	if d.currentName != filename {
		return nil
	}
	return d.closeCurrentLocked()
}

// closeCurrentLocked 关闭并清空当前文件句柄。调用方必须先持有 d.mu。
func (d *File) closeCurrentLocked() error {
	if d.currentFile == nil {
		d.currentName = ""
		return nil
	}
	err := d.currentFile.Close()
	d.currentFile = nil
	d.currentName = ""
	return err
}

func (d *File) ensureDir() error {
	return os.MkdirAll(d.path, 0o755)
}

// getLogFile 获取当前日志文件路径，并在达到大小阈值时切换轮转文件。
func (d *File) getLogFile(current time.Time) string {
	date := current.Format("2006-01-02")
	base := filepath.Join(d.path, fmt.Sprintf("%s.log", date))

	if d.maxFileSize <= 0 {
		return base
	}

	info, err := os.Stat(base)
	if err != nil || info.Size() < d.maxFileSize {
		return base
	}

	for index := 1; ; index++ {
		rotated := filepath.Join(d.path, fmt.Sprintf("%s_%d.log", date, index))
		rotatedInfo, statErr := os.Stat(rotated)
		if os.IsNotExist(statErr) {
			return rotated
		}
		if statErr == nil && rotatedInfo.Size() < d.maxFileSize {
			return rotated
		}
	}
}
