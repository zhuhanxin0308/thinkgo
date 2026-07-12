package driver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"thinkgo/framework/log"
)

const (
	DefaultMaxFileSize   int64 = 10 * 1024 * 1024
	DefaultRetentionDays       = 30
	DefaultMaxFiles            = 100
	DefaultMaxTotalSize  int64 = 1024 * 1024 * 1024
	fileCleanupInterval        = time.Hour
)

// FileOptions 定义文件轮转和磁盘容量治理边界；零值使用安全默认值。
type FileOptions struct {
	MaxFileSize   int64
	RetentionDays int
	MaxFiles      int
	MaxTotalSize  int64
}

// File 文件日志驱动，支持按日期、大小轮转以及天数、数量、总容量三重治理。
type File struct {
	path              string
	maxFileSize       int64
	retentionDays     int
	maxFiles          int
	maxTotalSize      int64
	mu                sync.Mutex
	currentName       string
	currentFile       *os.File
	currentSize       int64
	lastCleanupTime   time.Time
	initializationErr error
}

type managedLogFile struct {
	path    string
	name    string
	date    time.Time
	modTime time.Time
	size    int64
}

// NewFile 创建采用安全默认治理参数的文件驱动；需要接收初始化错误时使用 NewFileWithOptions。
func NewFile(path string, maxFileSize ...int64) *File {
	options := FileOptions{}
	if len(maxFileSize) > 0 {
		options.MaxFileSize = maxFileSize[0]
	}
	normalized, err := normalizeFileOptions(options)
	driver := newFile(path, normalized)
	if strings.TrimSpace(path) == "" {
		err = errors.New("日志目录不能为空")
	}
	driver.initializationErr = err
	return driver
}

// NewFileWithOptions 校验配置、创建目录并立即应用最小权限。
func NewFileWithOptions(path string, options FileOptions) (*File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("日志目录不能为空")
	}
	normalized, err := normalizeFileOptions(options)
	if err != nil {
		return nil, err
	}
	driver := newFile(path, normalized)
	if err := driver.ensureDir(); err != nil {
		return nil, fmt.Errorf("初始化日志目录失败: %w", err)
	}
	return driver, nil
}

func newFile(path string, options FileOptions) *File {
	cleanedPath := strings.TrimSpace(path)
	if cleanedPath != "" {
		cleanedPath = filepath.Clean(cleanedPath)
	}
	return &File{
		path:          cleanedPath,
		maxFileSize:   options.MaxFileSize,
		retentionDays: options.RetentionDays,
		maxFiles:      options.MaxFiles,
		maxTotalSize:  options.MaxTotalSize,
	}
}

func normalizeFileOptions(options FileOptions) (FileOptions, error) {
	if options.MaxFileSize < 0 || options.RetentionDays < 0 || options.MaxFiles < 0 || options.MaxTotalSize < 0 {
		return FileOptions{}, errors.New("日志轮转与保留参数不能为负数")
	}
	if options.MaxFileSize == 0 {
		options.MaxFileSize = DefaultMaxFileSize
	}
	if options.RetentionDays == 0 {
		options.RetentionDays = DefaultRetentionDays
	}
	if options.MaxFiles == 0 {
		options.MaxFiles = DefaultMaxFiles
	}
	if options.MaxTotalSize == 0 {
		options.MaxTotalSize = DefaultMaxTotalSize
	}
	return options, nil
}

// WriteEntry 立即写入并同步单条日志，返回写入、同步或清理错误。
func (d *File) WriteEntry(entry *log.LogEntry) error {
	if d.initializationErr != nil {
		return d.initializationErr
	}
	if entry == nil {
		return errors.New("日志条目不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureDir(); err != nil {
		return err
	}
	line := entry.FormatEntry() + "\n"
	filename, err := d.getLogFileForSize(entry.Time, int64(len(line)))
	if err != nil {
		return err
	}
	file, err := d.getOrCreateFileLocked(filename)
	if err != nil {
		return err
	}
	written, err := file.WriteString(line)
	d.currentSize += int64(written)
	if err != nil {
		return errors.Join(err, d.closeFileLocked(filename))
	}
	if written != len(line) {
		return errors.Join(io.ErrShortWrite, d.closeFileLocked(filename))
	}
	if err = file.Sync(); err != nil {
		return errors.Join(err, d.closeFileLocked(filename))
	}
	return d.maybeCleanupLocked(time.Now())
}

// SaveEntries 按日期顺序批量写入，每个目标文件仅同步一次。
func (d *File) SaveEntries(entries []*log.LogEntry) error {
	if d.initializationErr != nil {
		return d.initializationErr
	}
	if len(entries) == 0 {
		return nil
	}
	groups := make(map[string][]*log.LogEntry)
	for _, entry := range entries {
		if entry == nil {
			return errors.New("日志条目不能为空")
		}
		date := entry.Time.Format("2006-01-02")
		groups[date] = append(groups[date], entry)
	}
	dates := make([]string, 0, len(groups))
	for date := range groups {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ensureDir(); err != nil {
		return err
	}
	for _, date := range dates {
		dateEntries := groups[date]
		for _, entry := range dateEntries {
			line := entry.FormatEntry() + "\n"
			filename, err := d.getLogFileForSize(entry.Time, int64(len(line)))
			if err != nil {
				return err
			}
			file, err := d.getOrCreateFileLocked(filename)
			if err != nil {
				return err
			}
			written, err := file.WriteString(line)
			d.currentSize += int64(written)
			if err != nil {
				return errors.Join(err, d.closeFileLocked(filename))
			}
			if written != len(line) {
				return errors.Join(io.ErrShortWrite, d.closeFileLocked(filename))
			}
		}
	}
	if d.currentFile != nil {
		if err := d.currentFile.Sync(); err != nil {
			return errors.Join(err, d.closeCurrentLocked())
		}
	}
	return d.maybeCleanupLocked(time.Now())
}

// Close 关闭当前缓存句柄。
func (d *File) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closeCurrentLocked()
}

func (d *File) getOrCreateFileLocked(filename string) (*os.File, error) {
	if d.currentFile != nil && d.currentName == filename {
		return d.currentFile, nil
	}
	if d.currentFile != nil {
		syncErr := d.currentFile.Sync()
		closeErr := d.closeCurrentLocked()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return nil, fmt.Errorf("同步并关闭上一日志文件失败: %w", err)
		}
	}

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := restrictLogPath(filename, false); err != nil {
		return nil, errors.Join(fmt.Errorf("限制日志文件权限失败: %w", err), file.Close())
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	d.currentName = filename
	d.currentFile = file
	d.currentSize = info.Size()
	// 新日志文件会改变数量和容量，强制本次写入后重新执行治理。
	d.lastCleanupTime = time.Time{}
	return file, nil
}

func (d *File) closeFileLocked(filename string) error {
	if d.currentName != filename {
		return nil
	}
	return d.closeCurrentLocked()
}

func (d *File) closeCurrentLocked() error {
	if d.currentFile == nil {
		d.currentName = ""
		d.currentSize = 0
		return nil
	}
	err := d.currentFile.Close()
	d.currentFile = nil
	d.currentName = ""
	d.currentSize = 0
	return err
}

func (d *File) ensureDir() error {
	if err := os.MkdirAll(d.path, 0o700); err != nil {
		return err
	}
	return restrictLogPath(d.path, true)
}

// getLogFile 返回可继续追加的日期或轮转日志文件，并传播非“不存在”类文件系统错误。
func (d *File) getLogFile(current time.Time) (string, error) {
	return d.getLogFileForSize(current, 0)
}

func (d *File) getLogFileForSize(current time.Time, incomingSize int64) (string, error) {
	date := current.Format("2006-01-02")
	base := filepath.Join(d.path, date+".log")
	if d.currentFile != nil && isLogFileForDate(d.currentName, date) && hasLogFileCapacity(d.currentSize, incomingSize, d.maxFileSize) {
		return d.currentName, nil
	}
	info, err := os.Stat(base)
	if os.IsNotExist(err) {
		return base, nil
	}
	if err != nil {
		return "", err
	}
	if hasLogFileCapacity(info.Size(), incomingSize, d.maxFileSize) {
		return base, nil
	}

	for index := 1; ; index++ {
		rotated := filepath.Join(d.path, fmt.Sprintf("%s_%d.log", date, index))
		rotatedInfo, statErr := os.Stat(rotated)
		if os.IsNotExist(statErr) {
			return rotated, nil
		}
		if statErr != nil {
			return "", statErr
		}
		if hasLogFileCapacity(rotatedInfo.Size(), incomingSize, d.maxFileSize) {
			return rotated, nil
		}
	}
}

func hasLogFileCapacity(currentSize int64, incomingSize int64, maxFileSize int64) bool {
	if incomingSize <= 0 {
		return currentSize < maxFileSize
	}
	if currentSize == 0 {
		return true
	}
	return currentSize <= maxFileSize-incomingSize
}

func isLogFileForDate(filename string, date string) bool {
	base := strings.TrimSuffix(filepath.Base(filename), ".log")
	return base == date || strings.HasPrefix(base, date+"_")
}

func (d *File) maybeCleanupLocked(now time.Time) error {
	if !d.lastCleanupTime.IsZero() && !now.Before(d.lastCleanupTime) && now.Sub(d.lastCleanupTime) < fileCleanupInterval {
		return nil
	}
	if err := d.cleanupLocked(now); err != nil {
		return err
	}
	d.lastCleanupTime = now
	return nil
}

func (d *File) cleanupLocked(now time.Time) error {
	files, err := d.managedLogFilesLocked(now.Location())
	if err != nil {
		return err
	}
	currentDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	cutoff := currentDate.AddDate(0, 0, -d.retentionDays)
	for index := 0; index < len(files); {
		file := files[index]
		if file.date.Before(cutoff) && !d.isCurrentFile(file.path) {
			if err := os.Remove(file.path); err != nil {
				return fmt.Errorf("删除过期日志 %s 失败: %w", file.name, err)
			}
			files = append(files[:index], files[index+1:]...)
			continue
		}
		index++
	}

	for len(files) > d.maxFiles {
		var removed bool
		files, removed, err = d.removeOldestLocked(files)
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("当前日志文件数量 %d 超过限制 %d 且无可清理文件", len(files), d.maxFiles)
		}
	}

	totalSize := managedFilesSize(files)
	for totalSize > d.maxTotalSize {
		var removed bool
		files, removed, err = d.removeOldestLocked(files)
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("当前日志总容量 %d 超过限制 %d 且无可清理文件", totalSize, d.maxTotalSize)
		}
		totalSize = managedFilesSize(files)
	}
	return nil
}

func (d *File) managedLogFilesLocked(location *time.Location) ([]managedLogFile, error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, err
	}
	files := make([]managedLogFile, 0, len(entries))
	for _, entry := range entries {
		date, managed := parseManagedLogName(entry.Name(), location)
		if entry.IsDir() || !managed {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, managedLogFile{
			path: filepath.Join(d.path, entry.Name()), name: entry.Name(), date: date,
			modTime: info.ModTime(), size: info.Size(),
		})
	}
	sort.Slice(files, func(left, right int) bool {
		if files[left].date.Equal(files[right].date) {
			if files[left].modTime.Equal(files[right].modTime) {
				return files[left].name < files[right].name
			}
			return files[left].modTime.Before(files[right].modTime)
		}
		return files[left].date.Before(files[right].date)
	})
	return files, nil
}

func parseManagedLogName(name string, location *time.Location) (time.Time, bool) {
	if filepath.Ext(name) != ".log" {
		return time.Time{}, false
	}
	base := strings.TrimSuffix(name, ".log")
	if len(base) < len("2006-01-02") {
		return time.Time{}, false
	}
	date, err := time.ParseInLocation("2006-01-02", base[:10], location)
	if err != nil {
		return time.Time{}, false
	}
	remainder := base[10:]
	if remainder == "" {
		return date, true
	}
	if !strings.HasPrefix(remainder, "_") {
		return time.Time{}, false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(remainder, "_"))
	return date, err == nil && index > 0
}

func (d *File) removeOldestLocked(files []managedLogFile) ([]managedLogFile, bool, error) {
	for index, file := range files {
		if d.isCurrentFile(file.path) {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			return files, false, fmt.Errorf("删除日志 %s 失败: %w", file.name, err)
		}
		return append(files[:index], files[index+1:]...), true, nil
	}
	return files, false, nil
}

func (d *File) isCurrentFile(path string) bool {
	return d.currentName != "" && filepath.Clean(path) == filepath.Clean(d.currentName)
}

func managedFilesSize(files []managedLogFile) int64 {
	var size int64
	for _, file := range files {
		size += file.size
	}
	return size
}
