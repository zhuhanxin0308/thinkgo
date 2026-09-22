package filesystem

import (
	"crypto/md5"  // #nosec G501 -- ThinkPHP/Flysystem 文件校验兼容算法，不用于密码、签名或认证。
	"crypto/sha1" // #nosec G505 -- ThinkPHP/Flysystem 可选文件校验算法，不用于密码、签名或认证。
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	// EntryTypeFile 对应 Flysystem FileAttributes。
	EntryTypeFile = "file"
	// EntryTypeDirectory 对应 Flysystem DirectoryAttributes。
	EntryTypeDirectory = "dir"
)

type localFileOperations struct {
	copy           func(io.Writer, io.Reader) (int64, error)
	chmod          func(*os.Root, string, os.FileMode) error
	syncFile       func(*os.File) error
	closeTemporary func(*os.File) error
	replace        func(*os.Root, string, string) error
	closeSource    func(io.Closer) error
	syncDirectory  func(*os.Root, string) error
}

func defaultLocalFileOperations() localFileOperations {
	return localFileOperations{
		copy: io.Copy,
		chmod: func(root *os.Root, name string, mode os.FileMode) error {
			return root.Chmod(name, mode)
		},
		syncFile: func(file *os.File) error {
			return file.Sync()
		},
		closeTemporary: func(file *os.File) error {
			return file.Close()
		},
		replace: func(root *os.Root, oldName string, newName string) error {
			return root.Rename(oldName, newName)
		},
		closeSource: func(source io.Closer) error {
			return source.Close()
		},
		syncDirectory: syncLocalDirectory,
	}
}

// Entry 是 listContents 返回的文件或目录属性。
type Entry struct {
	Type         string
	Path         string
	FileSize     int64
	Visibility   string
	LastModified int64
	MimeType     string
}

// Local 使用受根目录约束的 os.Root 实现 ThinkPHP Local 驱动。
type Local struct {
	mu                   sync.RWMutex
	metadataMu           sync.RWMutex
	root                 *os.Root
	rootPath             string
	url                  string
	configuration        LocalConfig
	links                linkHandling
	visibilityConfigured bool
	visibilityOverrides  map[string]string
	operations           localFileOperations
	closed               bool
	closeError           error
}

// NewLocal 创建本地磁盘。与 LocalFilesystemAdapter 一致，构造驱动时创建根目录。
func NewLocal(configuration LocalConfig) (*Local, error) {
	visibilityConfigured := configuration.Visibility != ""
	normalized, err := normalizeLocalConfig(configuration)
	if err != nil {
		return nil, err
	}
	rootPath, err := filepath.Abs(normalized.Root)
	if err != nil {
		return nil, fmt.Errorf("解析本地磁盘根目录失败: %w", err)
	}
	rootPath = filepath.Clean(rootPath)
	if err = os.MkdirAll(rootPath, normalized.directoryMode(normalized.Visibility)); err != nil {
		return nil, fmt.Errorf("创建本地磁盘根目录失败: %w", err)
	}
	information, err := os.Stat(rootPath)
	if err != nil {
		return nil, fmt.Errorf("读取本地磁盘根目录失败: %w", err)
	}
	if !information.IsDir() {
		return nil, fmt.Errorf("%w: local.root 不是目录", ErrInvalidConfiguration)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("打开本地磁盘根目录失败: %w", err)
	}
	return &Local{
		root:                 root,
		rootPath:             rootPath,
		url:                  normalized.URL,
		configuration:        normalized,
		links:                normalized.linkHandling(),
		visibilityConfigured: visibilityConfigured,
		visibilityOverrides:  make(map[string]string),
		operations:           defaultLocalFileOperations(),
	}, nil
}

// Write 写入字符串内容，对应 Flysystem write。
func (disk *Local) Write(filePath string, contents string, options ...map[string]interface{}) error {
	return disk.writeReader(filePath, strings.NewReader(contents), options)
}

// WriteBytes 是 Go 二进制业务代码的便捷入口，公开 Write 的语义保持不变。
func (disk *Local) WriteBytes(filePath string, contents []byte, options ...map[string]interface{}) error {
	return disk.writeReader(filePath, strings.NewReader(string(contents)), options)
}

// WriteStream 写入流，并与 Flysystem 一致在可定位流上从起点开始读取。
func (disk *Local) WriteStream(filePath string, contents io.Reader, options ...map[string]interface{}) error {
	if contents == nil {
		return fmt.Errorf("%w: 写入流不能为空", ErrInvalidConfiguration)
	}
	if seeker, ok := contents.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("重置写入流失败: %w", err)
		}
	}
	return disk.writeReader(filePath, contents, options)
}

func (disk *Local) writeReader(filePath string, contents io.Reader, optionSets []map[string]interface{}) error {
	return disk.writeReaderWithSource(filePath, contents, nil, "", optionSets)
}

// Read 读取完整字符串内容。
func (disk *Local) Read(filePath string) (string, error) {
	content, err := disk.ReadBytes(filePath)
	return string(content), err
}

// ReadBytes 返回文件的原始字节内容。
func (disk *Local) ReadBytes(filePath string) ([]byte, error) {
	logicalPath, err := normalizeFilePath(filePath)
	if err != nil {
		return nil, err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return nil, err
	}
	content, err := disk.root.ReadFile(logicalPath)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	return content, nil
}

// ReadStream 打开只读文件流，调用方负责关闭。
func (disk *Local) ReadStream(filePath string) (io.ReadCloser, error) {
	logicalPath, err := normalizeFilePath(filePath)
	if err != nil {
		return nil, err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return nil, err
	}
	opened, err := disk.root.Open(logicalPath)
	if err != nil {
		return nil, fmt.Errorf("打开读取流失败: %w", err)
	}
	return opened, nil
}

// FileExists 判断路径是否为文件。
func (disk *Local) FileExists(filePath string) (bool, error) {
	logicalPath, err := normalizeFilePath(filePath)
	if err != nil {
		return false, err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return false, err
	}
	information, err := disk.root.Stat(logicalPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查文件是否存在失败: %w", err)
	}
	return information.Mode().IsRegular(), nil
}

// DirectoryExists 判断路径是否为目录；空路径代表磁盘根目录。
func (disk *Local) DirectoryExists(directoryPath string) (bool, error) {
	logicalPath, err := normalizePath(directoryPath)
	if err != nil {
		return false, err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return false, err
	}
	information, err := disk.root.Stat(rootName(logicalPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查目录是否存在失败: %w", err)
	}
	return information.IsDir(), nil
}

// Has 判断路径是文件或目录。
func (disk *Local) Has(location string) (bool, error) {
	logicalPath, err := normalizePath(location)
	if err != nil {
		return false, err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return false, err
	}
	_, err = disk.root.Stat(rootName(logicalPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查路径是否存在失败: %w", err)
	}
	return true, nil
}

// Delete 删除文件；目标不存在时与 Flysystem 一致直接成功。
func (disk *Local) Delete(filePath string) error {
	logicalPath, err := normalizeFilePath(filePath)
	if err != nil {
		return err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return err
	}
	information, err := disk.root.Stat(logicalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取删除目标失败: %w", err)
	}
	if information.IsDir() {
		return fmt.Errorf("删除文件目标是目录: %s", logicalPath)
	}
	if err = disk.root.Remove(logicalPath); err != nil {
		return fmt.Errorf("删除文件失败: %w", err)
	}
	disk.forgetVisibility(logicalPath, false)
	return nil
}

// DeleteDirectory 递归删除目录；目标不存在或不是目录时直接成功。
func (disk *Local) DeleteDirectory(directoryPath string) error {
	logicalPath, err := normalizeFilePath(directoryPath)
	if err != nil {
		return err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return err
	}
	information, err := disk.root.Stat(logicalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取删除目录失败: %w", err)
	}
	if !information.IsDir() {
		return nil
	}
	if err = disk.root.RemoveAll(logicalPath); err != nil {
		return fmt.Errorf("删除目录失败: %w", err)
	}
	disk.forgetVisibility(logicalPath, true)
	return nil
}

// CreateDirectory 递归创建目录。
func (disk *Local) CreateDirectory(directoryPath string, optionSets ...map[string]interface{}) error {
	logicalPath, err := normalizePath(directoryPath)
	if err != nil {
		return err
	}
	options, err := singleOptions(optionSets)
	if err != nil {
		return err
	}
	visibility, visibilityExplicit, err := disk.writeVisibility(options)
	if err != nil {
		return err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return err
	}
	target := rootName(logicalPath)
	if err = disk.root.MkdirAll(target, disk.configuration.directoryMode(visibility)); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	if visibilityExplicit {
		if err = disk.root.Chmod(target, disk.configuration.directoryMode(visibility)); err != nil {
			return fmt.Errorf("设置目录可见性失败: %w", err)
		}
	}
	disk.rememberVisibility(logicalPath, visibility)
	return nil
}

// ListContents 列出目录内容，deep=false 为浅层，deep=true 为递归。
func (disk *Local) ListContents(directoryPath string, deep bool) ([]Entry, error) {
	logicalPath, err := normalizePath(directoryPath)
	if err != nil {
		return nil, err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return nil, err
	}
	if err = disk.ensureListPathHasNoLinksLocked(logicalPath); err != nil {
		return nil, err
	}
	start := rootName(logicalPath)
	information, err := disk.root.Stat(start)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !information.IsDir()) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取列表目录失败: %w", err)
	}
	entries := make([]Entry, 0)
	if deep {
		err = fs.WalkDir(disk.root.FS(), start, func(currentPath string, directoryEntry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if currentPath == start {
				return nil
			}
			if directoryEntry.Type()&os.ModeSymlink != 0 {
				if disk.links == skipLinks {
					if directoryEntry.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				return fmt.Errorf("%w: %s", ErrSymbolicLink, currentPath)
			}
			entry, entryErr := disk.entryFromDirectoryEntry(currentPath, directoryEntry)
			if entryErr != nil {
				return entryErr
			}
			entries = append(entries, entry)
			return nil
		})
	} else {
		var children []fs.DirEntry
		children, err = fs.ReadDir(disk.root.FS(), start)
		if err == nil {
			for _, child := range children {
				currentPath := child.Name()
				if logicalPath != "" {
					currentPath = path.Join(logicalPath, child.Name())
				}
				if child.Type()&os.ModeSymlink != 0 {
					if disk.links == skipLinks {
						continue
					}
					return nil, fmt.Errorf("%w: %s", ErrSymbolicLink, currentPath)
				}
				entry, entryErr := disk.entryFromDirectoryEntry(currentPath, child)
				if entryErr != nil {
					return nil, entryErr
				}
				entries = append(entries, entry)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("列出目录内容失败: %w", err)
	}
	sort.Slice(entries, func(left int, right int) bool { return entries[left].Path < entries[right].Path })
	return entries, nil
}

// Move 移动文件或目录。
func (disk *Local) Move(source string, destination string, optionSets ...map[string]interface{}) error {
	from, err := normalizeFilePath(source)
	if err != nil {
		return err
	}
	to, err := normalizeFilePath(destination)
	if err != nil {
		return err
	}
	options, err := singleOptions(optionSets)
	if err != nil {
		return err
	}
	explicitVisibility, hasExplicitVisibility, err := optionVisibility(options, "visibility", "", false)
	if err != nil {
		return err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return err
	}
	information, err := disk.root.Stat(from)
	if err != nil {
		return fmt.Errorf("读取移动源失败: %w", err)
	}
	directoryVisibility := disk.configuration.Visibility
	if information.IsDir() {
		directoryVisibility = disk.configuration.Visibility
	}
	if err = disk.ensureParentDirectoryLocked(to, directoryVisibility); err != nil {
		return err
	}
	if err = disk.operations.replace(disk.root, from, to); err != nil {
		return fmt.Errorf("移动路径失败: %w", err)
	}
	disk.moveRememberedVisibility(from, to)
	postCommitErr := error(nil)
	if hasExplicitVisibility {
		mode := disk.configuration.fileMode(explicitVisibility)
		if information.IsDir() {
			mode = disk.configuration.directoryMode(explicitVisibility)
		}
		if err = disk.operations.chmod(disk.root, to, mode); err != nil {
			postCommitErr = fmt.Errorf("设置移动目标可见性失败: %w", err)
		} else {
			disk.rememberVisibility(to, explicitVisibility)
		}
	}
	destinationParent := path.Dir(to)
	postCommitErr = errors.Join(
		postCommitErr,
		wrapOperationError("同步移动目标目录失败", disk.operations.syncDirectory(disk.root, destinationParent)),
	)
	sourceParent := path.Dir(from)
	if sourceParent != destinationParent {
		postCommitErr = errors.Join(
			postCommitErr,
			wrapOperationError("同步移动源目录失败", disk.operations.syncDirectory(disk.root, sourceParent)),
		)
	}
	if postCommitErr != nil {
		return errors.Join(ErrFilesystemOperationCommitted, postCommitErr)
	}
	return nil
}

// Copy 复制文件，并默认保留源文件可见性。
func (disk *Local) Copy(source string, destination string, optionSets ...map[string]interface{}) error {
	from, err := normalizeFilePath(source)
	if err != nil {
		return err
	}
	to, err := normalizeFilePath(destination)
	if err != nil {
		return err
	}
	options, err := singleOptions(optionSets)
	if err != nil {
		return err
	}
	if from == to {
		return nil
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return err
	}
	sourceInformation, err := disk.root.Stat(from)
	if err != nil {
		return fmt.Errorf("读取复制源失败: %w", err)
	}
	if !sourceInformation.Mode().IsRegular() {
		return fmt.Errorf("复制源不是文件: %s", from)
	}
	visibility, err := disk.copyVisibilityLocked(from, sourceInformation, options)
	if err != nil {
		return err
	}
	if err = disk.ensureParentDirectoryLocked(to, disk.configuration.Visibility); err != nil {
		return err
	}
	input, err := disk.root.Open(from)
	if err != nil {
		return fmt.Errorf("打开复制源失败: %w", err)
	}
	committed, err := disk.replaceReaderLocked(
		to,
		input,
		input,
		disk.configuration.fileMode(visibility),
		"复制文件失败",
		"关闭复制源失败",
	)
	if committed {
		disk.rememberVisibility(to, visibility)
	}
	return err
}

// LastModified 返回 Unix 时间戳，与 Flysystem lastModified 返回值一致。
func (disk *Local) LastModified(filePath string) (int64, error) {
	information, _, err := disk.stat(filePath, false)
	if err != nil {
		return 0, err
	}
	return information.ModTime().Unix(), nil
}

// FileSize 返回文件字节数。
func (disk *Local) FileSize(filePath string) (int64, error) {
	information, logicalPath, err := disk.stat(filePath, true)
	if err != nil {
		return 0, err
	}
	if !information.Mode().IsRegular() {
		return 0, fmt.Errorf("文件大小目标不是文件: %s", logicalPath)
	}
	return information.Size(), nil
}

// MimeType 读取文件前 512 字节进行标准 MIME 探测。
func (disk *Local) MimeType(filePath string) (string, error) {
	logicalPath, err := normalizeFilePath(filePath)
	if err != nil {
		return "", err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return "", err
	}
	opened, err := disk.root.Open(logicalPath)
	if err != nil {
		return "", fmt.Errorf("打开 MIME 探测文件失败: %w", err)
	}
	buffer := make([]byte, 512)
	readBytes, readErr := opened.Read(buffer)
	closeErr := opened.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", errors.Join(fmt.Errorf("读取 MIME 探测内容失败: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("关闭 MIME 探测文件失败: %w", closeErr)
	}
	return http.DetectContentType(buffer[:readBytes]), nil
}

// SetVisibility 设置文件或目录的 public/private 权限。
func (disk *Local) SetVisibility(filePath string, visibility string) error {
	if err := validateVisibility(visibility); err != nil {
		return err
	}
	logicalPath, err := normalizePath(filePath)
	if err != nil {
		return err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return err
	}
	information, err := disk.root.Stat(rootName(logicalPath))
	if err != nil {
		return fmt.Errorf("读取可见性目标失败: %w", err)
	}
	mode := disk.configuration.fileMode(visibility)
	if information.IsDir() {
		mode = disk.configuration.directoryMode(visibility)
	}
	if err = disk.root.Chmod(rootName(logicalPath), mode); err != nil {
		return fmt.Errorf("设置路径可见性失败: %w", err)
	}
	disk.rememberVisibility(logicalPath, visibility)
	return nil
}

// Visibility 返回文件或目录的 public/private 可见性。
func (disk *Local) Visibility(filePath string) (string, error) {
	information, logicalPath, err := disk.stat(filePath, false)
	if err != nil {
		return "", err
	}
	return disk.resolveRememberedVisibility(logicalPath, information), nil
}

// Checksum 计算文件校验值，默认算法与 LocalFilesystemAdapter 一致为 md5。
func (disk *Local) Checksum(filePath string, optionSets ...map[string]interface{}) (string, error) {
	logicalPath, err := normalizeFilePath(filePath)
	if err != nil {
		return "", err
	}
	options, err := singleOptions(optionSets)
	if err != nil {
		return "", err
	}
	algorithm := "md5"
	if raw, exists := options["checksum_algo"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%w: checksum_algo 必须是非空字符串", ErrInvalidConfiguration)
		}
		algorithm = strings.ToLower(strings.TrimSpace(value))
	}
	digest, err := checksumHash(algorithm)
	if err != nil {
		return "", err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return "", err
	}
	opened, err := disk.root.Open(logicalPath)
	if err != nil {
		return "", fmt.Errorf("打开校验文件失败: %w", err)
	}
	_, copyErr := io.Copy(digest, opened)
	closeErr := opened.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.Join(wrapOperationError("计算文件校验值失败", copyErr), wrapOperationError("关闭校验文件失败", closeErr))
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Path 返回磁盘根目录下的完整路径。
func (disk *Local) Path(filePath string) (string, error) {
	logicalPath, err := normalizePath(filePath)
	if err != nil {
		return "", err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return "", err
	}
	if logicalPath == "" {
		return disk.rootPath, nil
	}
	return filepath.Join(disk.rootPath, filepath.FromSlash(logicalPath)), nil
}

// URL 使用磁盘 url 前缀生成外部访问地址。
func (disk *Local) URL(filePath string) (string, error) {
	logicalPath, err := normalizePath(filePath)
	if err != nil {
		return "", err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return "", err
	}
	if disk.url == "" {
		return "", ErrURLNotSupported
	}
	return strings.TrimRight(disk.url, "/") + "/" + strings.TrimLeft(logicalPath, "/"), nil
}

// Close 幂等关闭根目录句柄。
func (disk *Local) Close() error {
	if disk == nil {
		return nil
	}
	disk.mu.Lock()
	defer disk.mu.Unlock()
	if disk.closed {
		return disk.closeError
	}
	disk.closed = true
	if disk.root != nil {
		disk.closeError = disk.root.Close()
		disk.root = nil
	}
	return disk.closeError
}

func (disk *Local) ensureOpenLocked() error {
	if disk == nil || disk.closed || disk.root == nil {
		return ErrFilesystemClosed
	}
	return nil
}

func (disk *Local) ensureParentDirectoryLocked(logicalPath string, visibility string) error {
	parent := path.Dir(logicalPath)
	if parent == "." || parent == "" {
		return nil
	}
	if err := disk.root.MkdirAll(parent, disk.configuration.directoryMode(visibility)); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	return nil
}

func (disk *Local) ensureListPathHasNoLinksLocked(logicalPath string) error {
	if logicalPath == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(logicalPath, "/") {
		current = path.Join(current, part)
		information, err := disk.root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// Flysystem 对不存在的列表目录返回空结果，缺失路径不属于链接风险。
			return nil
		}
		if err != nil {
			return err
		}
		if information.Mode()&os.ModeSymlink != 0 {
			if disk.links == skipLinks {
				return nil
			}
			return fmt.Errorf("%w: %s", ErrSymbolicLink, current)
		}
	}
	return nil
}

func (disk *Local) entryFromDirectoryEntry(currentPath string, directoryEntry fs.DirEntry) (Entry, error) {
	information, err := directoryEntry.Info()
	if err != nil {
		return Entry{}, err
	}
	entryType := EntryTypeFile
	if information.IsDir() {
		entryType = EntryTypeDirectory
	}
	return Entry{
		Type:         entryType,
		Path:         filepath.ToSlash(currentPath),
		FileSize:     information.Size(),
		Visibility:   disk.resolveRememberedVisibility(filepath.ToSlash(currentPath), information),
		LastModified: information.ModTime().Unix(),
	}, nil
}

func (disk *Local) stat(filePath string, requireFilePath bool) (fs.FileInfo, string, error) {
	var logicalPath string
	var err error
	if requireFilePath {
		logicalPath, err = normalizeFilePath(filePath)
	} else {
		logicalPath, err = normalizePath(filePath)
	}
	if err != nil {
		return nil, "", err
	}
	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return nil, "", err
	}
	information, err := disk.root.Stat(rootName(logicalPath))
	if err != nil {
		return nil, "", fmt.Errorf("读取路径属性失败: %w", err)
	}
	return information, logicalPath, nil
}

func (disk *Local) writeVisibility(options map[string]interface{}) (string, bool, error) {
	return optionVisibility(options, "visibility", disk.configuration.Visibility, disk.visibilityConfigured)
}

func (disk *Local) copyVisibilityLocked(source string, information fs.FileInfo, options map[string]interface{}) (string, error) {
	if visibility, explicit, err := optionVisibility(options, "visibility", "", false); err != nil {
		return "", err
	} else if explicit {
		return visibility, nil
	}
	retainVisibility := true
	if raw, exists := options["retain_visibility"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return "", fmt.Errorf("%w: retain_visibility 必须是布尔值", ErrInvalidConfiguration)
		}
		retainVisibility = value
	}
	if retainVisibility {
		return disk.resolveRememberedVisibility(source, information), nil
	}
	return disk.configuration.Visibility, nil
}

func (disk *Local) resolveRememberedVisibility(logicalPath string, information fs.FileInfo) string {
	disk.metadataMu.RLock()
	visibility, remembered := disk.visibilityOverrides[logicalPath]
	disk.metadataMu.RUnlock()
	if remembered {
		return visibility
	}
	mode := information.Mode().Perm()
	if information.IsDir() {
		if mode == disk.configuration.Permissions.DirectoryPrivate {
			return VisibilityPrivate
		}
		return VisibilityPublic
	}
	if mode == disk.configuration.Permissions.FilePrivate {
		return VisibilityPrivate
	}
	return VisibilityPublic
}

func (disk *Local) rememberVisibility(logicalPath string, visibility string) {
	disk.metadataMu.Lock()
	disk.visibilityOverrides[logicalPath] = visibility
	disk.metadataMu.Unlock()
}

func (disk *Local) forgetVisibility(logicalPath string, recursive bool) {
	disk.metadataMu.Lock()
	defer disk.metadataMu.Unlock()
	delete(disk.visibilityOverrides, logicalPath)
	if !recursive {
		return
	}
	prefix := strings.TrimSuffix(logicalPath, "/") + "/"
	for name := range disk.visibilityOverrides {
		if strings.HasPrefix(name, prefix) {
			delete(disk.visibilityOverrides, name)
		}
	}
}

func (disk *Local) moveRememberedVisibility(source string, destination string) {
	disk.metadataMu.Lock()
	defer disk.metadataMu.Unlock()
	updates := make(map[string]string)
	prefix := strings.TrimSuffix(source, "/") + "/"
	for name, visibility := range disk.visibilityOverrides {
		if name == source {
			updates[destination] = visibility
			delete(disk.visibilityOverrides, name)
			continue
		}
		if strings.HasPrefix(name, prefix) {
			relative := strings.TrimPrefix(name, prefix)
			updates[path.Join(destination, relative)] = visibility
			delete(disk.visibilityOverrides, name)
		}
	}
	for name, visibility := range updates {
		disk.visibilityOverrides[name] = visibility
	}
}

func singleOptions(optionSets []map[string]interface{}) (map[string]interface{}, error) {
	if len(optionSets) > 1 {
		return nil, fmt.Errorf("%w: 配置数组最多只能传入一次", ErrInvalidConfiguration)
	}
	if len(optionSets) == 0 || optionSets[0] == nil {
		return map[string]interface{}{}, nil
	}
	return optionSets[0], nil
}

func optionVisibility(options map[string]interface{}, key string, fallback string, fallbackExplicit bool) (string, bool, error) {
	raw, exists := options[key]
	if !exists {
		return fallback, fallbackExplicit, nil
	}
	visibility, ok := raw.(string)
	if !ok {
		return "", false, fmt.Errorf("%w: %s 必须是字符串", ErrInvalidConfiguration, key)
	}
	if err := validateVisibility(visibility); err != nil {
		return "", false, err
	}
	return visibility, true, nil
}

func checksumHash(algorithm string) (hash.Hash, error) {
	switch algorithm {
	case "md5":
		return md5.New(), nil // #nosec G401 -- 兼容 LocalFilesystemAdapter 默认校验算法，不用于密码学认证。
	case "sha1":
		return sha1.New(), nil // #nosec G401 -- 兼容上传 hashName 与 Flysystem 校验配置，不用于密码学认证。
	case "sha256":
		return sha256.New(), nil
	case "sha384":
		return sha512.New384(), nil
	case "sha512":
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("%w: 不支持校验算法 %s", ErrInvalidConfiguration, algorithm)
	}
}

func rootName(logicalPath string) string {
	if logicalPath == "" {
		return "."
	}
	return logicalPath
}

func wrapOperationError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}
