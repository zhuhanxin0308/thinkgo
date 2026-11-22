package driver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/logcoord"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

const (
	DefaultMaxFileSize   int64 = 10 * 1024 * 1024
	DefaultRetentionDays       = 30
	DefaultMaxFiles            = 100
	DefaultMaxTotalSize  int64 = 1024 * 1024 * 1024
	fileCleanupInterval        = time.Hour
)

var errUnsafeLogFilePath = errors.New("日志文件路径必须指向当前打开的普通文件")

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
	directoryIdentity os.FileInfo
	maxFileSize       int64
	retentionDays     int
	maxFiles          int
	maxTotalSize      int64
	mu                sync.Mutex
	currentName       string
	currentFile       *os.File
	currentOwner      io.Closer
	currentSize       int64
	lastCleanupTime   time.Time
	initializationErr error
	location          *time.Location
}

type managedLogFile struct {
	path string
	name string
	date time.Time
	info os.FileInfo
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
		location:      time.UTC,
	}
}

// SetLocation 设置文件日志按应用时区进行保留和清理。
func (d *File) SetLocation(location *time.Location) {
	if d == nil {
		return
	}
	if location == nil {
		location = time.UTC
	}
	d.mu.Lock()
	d.location = location
	d.mu.Unlock()
}

// now 返回文件驱动使用的当前应用时间。
func (d *File) now() time.Time {
	location := d.location
	if location == nil {
		location = time.UTC
	}
	return time.Now().In(location)
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
	return d.withDirectoryWrite(func() error { return d.appendEntryLocked(entry) })
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

	return d.withDirectoryWrite(func() error {
		for _, date := range dates {
			for _, entry := range groups[date] {
				if err := d.appendEntryLocked(entry); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// withDirectoryWrite 把选段、写入、同步和清理置于同一目录锁内，其他驱动只能看到完整操作。
func (d *File) withDirectoryWrite(write func() error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ensureDir(); err != nil {
		return err
	}
	return withLogDirectoryLock(d.path, d.directoryIdentity, func() error {
		if err := d.refreshCurrentLocked(); err != nil {
			return err
		}
		if err := write(); err != nil {
			return err
		}
		if err := d.currentFile.Sync(); err != nil {
			return errors.Join(err, d.closeCurrentLocked())
		}
		return d.maybeCleanupLocked(d.now())
	})
}

func (d *File) appendEntryLocked(entry *log.LogEntry) error {
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
	if err == nil && written != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return errors.Join(err, d.closeCurrentLocked())
	}
	return nil
}

// Close 关闭当前缓存句柄。
func (d *File) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.currentOwner == nil {
		return d.closeCurrentLocked()
	}
	closed := false
	err := withLogDirectoryLock(d.path, d.directoryIdentity, func() error {
		closed = true
		return d.closeCurrentLocked()
	})
	if !closed {
		// 目录被外部维护移除时也必须释放本进程持有的两个句柄。
		err = errors.Join(err, d.releaseCurrentLocked())
	}
	return err
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

	file, err := openManagedLogFile(filename)
	if err != nil {
		return nil, err
	}
	info, matches, err := inspectLogHandle(filename, file)
	if err == nil && !matches {
		err = errUnsafeLogFilePath
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := restrictLogPath(filename, false); err != nil {
		return nil, errors.Join(fmt.Errorf("限制日志文件权限失败: %w", err), file.Close())
	}
	owner, err := retainLogOwner(logcoord.OwnerPath(filename))
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	d.currentOwner = owner
	d.currentName = filename
	d.currentFile = file
	d.currentSize = info.Size()
	// 新日志文件会改变数量和容量，强制本次写入后重新执行治理。
	d.lastCleanupTime = time.Time{}
	return file, nil
}

// openManagedLogFile 将独占创建与追加分开，拒绝悬空链接时不会创建链接目标。
func openManagedLogFile(filename string) (*os.File, error) {
	const appendFlags = os.O_APPEND | os.O_WRONLY
	// #nosec G304 -- filename 来自日志目录和轮转规则，独占创建不跟随已存在的链接。
	file, err := os.OpenFile(filename, appendFlags|os.O_CREATE|os.O_EXCL, 0o600)
	if !errors.Is(err, os.ErrExist) {
		return file, err
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errUnsafeLogFilePath
	}
	// #nosec G304 -- 仅追加现存文件，调用方还会在写入前复核句柄身份。
	return os.OpenFile(filename, appendFlags, 0)
}

// inspectLogHandle 区分合法的文件替换与不安全路径，返回当前句柄的真实大小。
func inspectLogHandle(filename string, file *os.File) (os.FileInfo, bool, error) {
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("获取已打开日志文件信息失败: %w", err)
	}
	pathInfo, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return openedInfo, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("检查日志文件路径失败: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || !openedInfo.Mode().IsRegular() {
		return nil, false, errUnsafeLogFilePath
	}
	return openedInfo, os.SameFile(openedInfo, pathInfo), nil
}

func (d *File) refreshCurrentLocked() error {
	if d.currentFile == nil {
		return nil
	}
	info, matches, err := inspectLogHandle(d.currentName, d.currentFile)
	if !matches {
		// 外部维护可能替换或移走了旧文件；关闭旧句柄后通过正常选段流程重新打开。
		return errors.Join(err, d.closeCurrentLocked())
	}
	if info.Size() != d.currentSize {
		d.lastCleanupTime = time.Time{}
	}
	d.currentSize = info.Size()
	return nil
}

func (d *File) closeCurrentLocked() error {
	name := d.currentName
	err := d.releaseCurrentLocked()
	if name == "" {
		return err
	}
	// 锁文件所有准入者须先取得目录锁，故可在独占探测完成并关闭句柄后安全回收其名称。
	_, ownerErr := retireIdleLogOwner(logcoord.OwnerPath(name))
	return errors.Join(err, ownerErr)
}

func (d *File) releaseCurrentLocked() error {
	if d.currentFile == nil {
		d.currentName = ""
		d.currentSize = 0
		return nil
	}
	err := errors.Join(d.currentFile.Close(), d.currentOwner.Close())
	d.currentFile = nil
	d.currentOwner = nil
	d.currentName = ""
	d.currentSize = 0
	return err
}

func (d *File) ensureDir() (result error) {
	// 合法配置可通过软链接选择日志目录；后续使用同一实际目录，不依赖配置别名继续指向原处。
	if d.directoryIdentity == nil {
		if err := os.MkdirAll(d.path, 0o700); err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(d.path)
		if err != nil {
			return err
		}
		d.path, err = filepath.Abs(resolved)
		if err != nil {
			return err
		}
	}
	directory, err := openLogDirectory(d.path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	info, err := verifyLogDirectory(d.path, directory, d.directoryIdentity)
	if err != nil {
		return err
	}
	if err := restrictLogPath(d.path, true); err != nil {
		return err
	}
	d.directoryIdentity = info
	return nil
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
	d.lastCleanupTime = now
	if err := d.cleanupLocked(now); err != nil {
		d.lastCleanupTime = time.Time{}
		return err
	}
	return nil
}

func (d *File) cleanupLocked(now time.Time) error {
	if err := pruneIdleLogOwners(d.path); err != nil {
		return err
	}
	files, err := d.managedLogFilesLocked(now.Location())
	if err != nil {
		return err
	}
	currentDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	cutoff := currentDate.AddDate(0, 0, -d.retentionDays)
	for index := 0; index < len(files); {
		file := files[index]
		if file.date.Before(cutoff) && !d.isCurrentFile(file.path) {
			removed, err := removeIdleLogFile(file)
			if err != nil {
				return fmt.Errorf("删除过期日志 %s 失败: %w", file.name, err)
			}
			if removed {
				files = append(files[:index], files[index+1:]...)
				continue
			}
			// 其他进程仍持有候选文件，下次写入重试，无需等满一小时。
			d.lastCleanupTime = time.Time{}
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
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(d.path, entry.Name())
		if runtime.GOOS == "windows" {
			// 部分 Windows 卷的目录项会按路径延迟获取文件编号，清理快照须当场保存句柄身份。
			info, err = logFileSnapshot(path)
			if err != nil {
				return nil, err
			}
		}
		files = append(files, managedLogFile{
			path: path, name: entry.Name(), date: date, info: info,
		})
	}
	sort.Slice(files, func(left, right int) bool {
		if files[left].date.Equal(files[right].date) {
			if files[left].info.ModTime().Equal(files[right].info.ModTime()) {
				return files[left].name < files[right].name
			}
			return files[left].info.ModTime().Before(files[right].info.ModTime())
		}
		return files[left].date.Before(files[right].date)
	})
	return files, nil
}

func logFileSnapshot(path string) (info os.FileInfo, result error) {
	// #nosec G304 -- 路径来自受管目录的日志枚举，打开后再次检查路径和句柄身份。
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	info, matches, err := inspectLogHandle(path, file)
	if err == nil && !matches {
		err = errUnsafeLogFilePath
	}
	return info, err
}

func parseManagedLogName(name string, location *time.Location) (time.Time, bool) {
	return logcoord.ManagedDate(name, location)
}

func (d *File) removeOldestLocked(files []managedLogFile) ([]managedLogFile, bool, error) {
	for index, file := range files {
		if d.isCurrentFile(file.path) {
			continue
		}
		removed, err := removeIdleLogFile(file)
		if err != nil {
			return files, false, fmt.Errorf("删除日志 %s 失败: %w", file.name, err)
		}
		if removed {
			return append(files[:index], files[index+1:]...), true, nil
		}
	}
	return files, false, nil
}

func (d *File) isCurrentFile(path string) bool {
	return d.currentName != "" && filepath.Clean(path) == filepath.Clean(d.currentName)
}

func managedFilesSize(files []managedLogFile) int64 {
	var size int64
	for _, file := range files {
		size += file.info.Size()
	}
	return size
}
