package driver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	maxFileCacheEntryBytes = 16 << 20
	maxFileLockBytes       = 4096
	staleTempFileAge       = time.Hour
	corruptLockRecoveryAge = time.Minute
	fileCacheLockShards    = 64
	counterLockTTL         = 30 * time.Second
	counterLockWait        = 2 * time.Second
	counterLockRetry       = 5 * time.Millisecond
	cacheLockKeyPrefix     = "__thinkgo_lock__:"
)

// File 是采用安全句柄复核、原子替换和分片进程锁的文件缓存驱动。
type File struct {
	path        string
	operationMu sync.RWMutex
	keyLocks    [fileCacheLockShards]sync.Mutex
}

type storedItem struct {
	Value  json.RawMessage `json:"value"`
	Expiry time.Time       `json:"expiry"`
}

type fileLockPayload struct {
	Owner  string    `json:"owner"`
	Expiry time.Time `json:"expiry"`
}

// NewFile 创建并验证文件缓存根目录。
func NewFile(path string) (*File, error) {
	path = strings.TrimSpace(path)
	if path == "" || containsControlCharacter(path) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCachePath, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCachePath, err)
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCachePath, err)
	}
	realPath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCachePath, err)
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCachePath, realPath)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		// #nosec G302 -- 缓存根目录按目录语义使用 0700，已禁止组和其他用户访问。
		if err = os.Chmod(realPath, 0o700); err != nil {
			return nil, fmt.Errorf("%w: 缓存目录权限过宽且无法收紧: %v", ErrInvalidCachePath, err)
		}
	}
	return &File{path: filepath.Clean(realPath)}, nil
}

func (c *File) Get(key string) (interface{}, bool, error) {
	if c == nil || c.path == "" {
		return nil, false, ErrInvalidCachePath
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	lock := c.keyLock(key)
	lock.Lock()
	defer lock.Unlock()
	item, found, err := c.readItemLocked(key, time.Now())
	if err != nil || !found {
		return nil, found, err
	}
	var value interface{}
	if err = json.Unmarshal(item.Value, &value); err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
	}
	return value, true, nil
}

func (c *File) Set(key string, value interface{}, ttl time.Duration) error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	expiry := time.Time{}
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	lock := c.keyLock(key)
	lock.Lock()
	defer lock.Unlock()
	return c.writeItemLocked(key, value, expiry)
}

func (c *File) Has(key string) (bool, error) {
	_, found, err := c.Get(key)
	return found, err
}

// CacheResourceIdentity 返回文件驱动的真实资源标识，用于运行时 store 隔离校验。
func (c *File) CacheResourceIdentity() string {
	if c == nil || c.path == "" {
		return ""
	}
	path := filepath.Clean(c.path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return "file:" + path
}

func (c *File) Delete(key string) error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	lock := c.keyLock(key)
	lock.Lock()
	defer lock.Unlock()
	path := c.cacheFilePath(key)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeCacheEntry
	}
	_, err = removeFileIfSame(path, info)
	return err
}

func (c *File) Clear() error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	entries, err := os.ReadDir(c.path)
	if err != nil {
		return err
	}
	now := time.Now()
	resultErr := error(nil)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		remove := isManagedCacheFilename(name)
		if strings.HasPrefix(name, ".tmp-cache-") {
			if info, infoErr := entry.Info(); infoErr == nil && now.Sub(info.ModTime()) >= staleTempFileAge {
				remove = true
			}
		}
		if remove {
			path := filepath.Join(c.path, name)
			info, infoErr := os.Lstat(path)
			if errors.Is(infoErr, os.ErrNotExist) {
				continue
			}
			if infoErr != nil {
				resultErr = errors.Join(resultErr, infoErr)
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				resultErr = errors.Join(resultErr, ErrUnsafeCacheEntry)
				continue
			}
			_, removeErr := removeFileIfSame(path, info)
			resultErr = errors.Join(resultErr, ignoreNotExist(removeErr))
		}
	}
	return resultErr
}

func (c *File) Inc(key string, step int64) (int64, error) {
	return c.changeCounter(key, step, false)
}

func (c *File) Dec(key string, step int64) (int64, error) {
	return c.changeCounter(key, step, true)
}

func (c *File) changeCounter(key string, step int64, subtract bool) (value int64, resultErr error) {
	if c == nil || c.path == "" {
		return 0, ErrInvalidCachePath
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	owner, err := newFileOperationOwner()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
	}
	lockKey := cacheLockKeyPrefix + key
	acquired, err := c.acquireCounterLock(lockKey, owner)
	if err != nil {
		return 0, err
	}
	if !acquired {
		return 0, ErrCacheLockBusy
	}
	defer func() {
		released, releaseErr := c.ReleaseLock(lockKey, owner)
		if releaseErr == nil && !released {
			releaseErr = ErrCacheLockLost
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	lock := c.keyLock(key)
	lock.Lock()
	defer lock.Unlock()

	item, found, err := c.readItemLocked(key, time.Now())
	if err != nil {
		return 0, err
	}
	current := int64(0)
	expiry := time.Time{}
	if found {
		var raw interface{}
		raw, err = decodeCounterJSON(item.Value)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
		}
		current, err = strictCounterValue(raw)
		if err != nil {
			return 0, err
		}
		expiry = item.Expiry
	}
	updated := int64(0)
	if subtract {
		updated, err = checkedCounterSubtract(current, step)
	} else {
		updated, err = checkedCounterAdd(current, step)
	}
	if err != nil {
		return 0, err
	}
	if err = c.writeItemLocked(key, updated, expiry); err != nil {
		return 0, err
	}
	return updated, nil
}

// acquireCounterLock 让文件计数的读改写跨进程串行化，避免原子替换掩盖丢失更新。
func (c *File) acquireCounterLock(key, owner string) (bool, error) {
	deadline := time.Now().Add(counterLockWait)
	for {
		acquired, err := c.AcquireLock(key, owner, counterLockTTL)
		if err != nil || acquired {
			return acquired, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		time.Sleep(counterLockRetry)
	}
}

// AcquireLock 通过独占创建锁文件实现跨进程锁，并完整校验锁载荷写入。
func (c *File) AcquireLock(key string, owner string, ttl time.Duration) (bool, error) {
	if c == nil || c.path == "" {
		return false, ErrInvalidCachePath
	}
	if owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	lock := c.keyLock("lock:" + key)
	lock.Lock()
	defer lock.Unlock()
	lockFile := c.lockFilePath(key)

	for {
		now := time.Now()
		// #nosec G304 -- lockFile 由缓存根目录和哈希键生成，并在独占创建前完成路径约束。
		handle, err := createCacheLockFile(lockFile)
		if err == nil {
			if err = lockCacheLockFile(handle); err != nil {
				return false, errors.Join(err, handle.Close())
			}
			if writeErr := writeCacheLockPayload(handle, fileLockPayload{Owner: owner, Expiry: now.Add(ttl)}); writeErr != nil {
				cleanupErr := discardCreatedCacheLockFile(lockFile, handle, nil)
				return false, errors.Join(writeErr, cleanupErr)
			}
			if closeErr := closeLockedCacheLockFile(handle); closeErr != nil {
				return false, closeErr
			}
			return true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}

		handle, info, openErr := c.openLockedCacheLockFile(lockFile)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return false, openErr
		}
		payload, readErr := readCacheLockPayload(handle)
		now = time.Now()
		if readErr != nil {
			if info != nil && now.Sub(info.ModTime()) >= corruptLockRecoveryAge {
				writeErr := writeCacheLockPayload(handle, fileLockPayload{Owner: owner, Expiry: now.Add(ttl)})
				closeErr := closeLockedCacheLockFile(handle)
				if combined := errors.Join(writeErr, closeErr); combined != nil {
					return false, combined
				}
				return true, nil
			}
			closeErr := closeLockedCacheLockFile(handle)
			if closeErr != nil {
				return false, closeErr
			}
			return false, fmt.Errorf("%w: %v", ErrInvalidCacheLock, readErr)
		}
		if payload.Expiry.After(now) {
			return false, closeLockedCacheLockFile(handle)
		}
		writeErr := writeCacheLockPayload(handle, fileLockPayload{Owner: owner, Expiry: now.Add(ttl)})
		closeErr := closeLockedCacheLockFile(handle)
		if combined := errors.Join(writeErr, closeErr); combined != nil {
			return false, combined
		}
		return true, nil
	}
}

// ReleaseLock 复核 owner、过期时间和文件身份后删除锁文件。
func (c *File) ReleaseLock(key string, owner string) (bool, error) {
	if c == nil || c.path == "" {
		return false, ErrInvalidCachePath
	}
	if owner == "" {
		return false, ErrInvalidCacheLock
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	lock := c.keyLock("lock:" + key)
	lock.Lock()
	defer lock.Unlock()
	lockFile := c.lockFilePath(key)
	handle, _, err := c.openLockedCacheLockFile(lockFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	payload, readErr := readCacheLockPayload(handle)
	if readErr != nil {
		return false, errors.Join(fmt.Errorf("%w: %v", ErrInvalidCacheLock, readErr), closeLockedCacheLockFile(handle))
	}
	if payload == nil {
		return false, closeLockedCacheLockFile(handle)
	}
	if payload.Owner != owner || !payload.Expiry.After(time.Now()) {
		return false, closeLockedCacheLockFile(handle)
	}
	writeErr := writeCacheLockPayload(handle, fileLockPayload{Owner: owner, Expiry: time.Unix(0, 0)})
	return writeErr == nil, errors.Join(writeErr, closeLockedCacheLockFile(handle))
}

// RenewLock 在当前 owner 尚未过期时持有同一文件句柄原地更新锁载荷，延长文件锁租约。
func (c *File) RenewLock(key string, owner string, ttl time.Duration) (bool, error) {
	if c == nil || c.path == "" {
		return false, ErrInvalidCachePath
	}
	if owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	c.operationMu.RLock()
	defer c.operationMu.RUnlock()
	lock := c.keyLock("lock:" + key)
	lock.Lock()
	defer lock.Unlock()
	lockFile := c.lockFilePath(key)
	handle, _, err := c.openLockedCacheLockFile(lockFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	payload, readErr := readCacheLockPayload(handle)
	if readErr != nil {
		return false, errors.Join(fmt.Errorf("%w: %v", ErrInvalidCacheLock, readErr), closeLockedCacheLockFile(handle))
	}
	if payload == nil {
		return false, closeLockedCacheLockFile(handle)
	}
	now := time.Now()
	if payload.Owner != owner || !payload.Expiry.After(now) {
		return false, closeLockedCacheLockFile(handle)
	}
	data, err := json.Marshal(fileLockPayload{Owner: owner, Expiry: now.Add(ttl)})
	if err != nil {
		return false, errors.Join(err, closeLockedCacheLockFile(handle))
	}
	if err = handle.Truncate(0); err == nil {
		_, err = handle.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = writeAll(handle, data)
	}
	if err != nil {
		return false, errors.Join(err, closeLockedCacheLockFile(handle))
	}
	err = handle.Sync()
	return err == nil, errors.Join(err, closeLockedCacheLockFile(handle))
}

func (c *File) readItemLocked(key string, now time.Time) (*storedItem, bool, error) {
	path := c.cacheFilePath(key)
	data, identity, found, err := c.readManagedFile(path, maxFileCacheEntryBytes)
	if err != nil || !found {
		return nil, found, err
	}
	var item storedItem
	if err = json.Unmarshal(data, &item); err != nil || len(item.Value) == 0 {
		return nil, false, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
	}
	if !item.Expiry.IsZero() && !now.Before(item.Expiry) {
		if _, removeErr := removeFileIfSame(path, identity); removeErr != nil {
			return nil, false, removeErr
		}
		return nil, false, nil
	}
	return &item, true, nil
}

func (c *File) writeItemLocked(key string, value interface{}, expiry time.Time) error {
	encodedValue, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data, err := json.Marshal(storedItem{Value: encodedValue, Expiry: expiry})
	if err != nil {
		return err
	}
	if len(data) > maxFileCacheEntryBytes {
		return ErrCacheEntryTooLarge
	}
	target := c.cacheFilePath(key)
	temporary, err := os.CreateTemp(c.path, ".tmp-cache-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if err = temporary.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if err = writeAll(temporary, data); err != nil {
		cleanup()
		return err
	}
	if err = temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err = temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	if err = replaceCacheFile(temporaryName, target); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	return nil
}

func (c *File) readManagedFile(path string, limit int64) ([]byte, os.FileInfo, bool, error) {
	return c.readManagedFileWithOpen(path, limit, os.Open)
}

func (c *File) readManagedFileWithOpen(path string, limit int64, open func(string) (*os.File, error)) ([]byte, os.FileInfo, bool, error) {
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, linkInfo, false, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return nil, linkInfo, false, ErrUnsafeCacheEntry
	}
	// #nosec G304 -- path 由缓存驱动的哈希文件名生成，并已通过 Lstat、普通文件和 SameFile 复核。
	handle, err := open(path)
	if err != nil {
		return nil, linkInfo, false, err
	}
	info, statErr := handle.Stat()
	verifiedInfo, verifyErr := os.Lstat(path)
	if statErr != nil || verifyErr != nil || !info.Mode().IsRegular() || verifiedInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, verifiedInfo) {
		_ = handle.Close()
		return nil, info, false, errors.Join(ErrUnsafeCacheEntry, statErr, verifyErr)
	}
	if info.Size() < 0 || info.Size() > limit {
		_ = handle.Close()
		return nil, info, false, fmt.Errorf("%w: %d", ErrCacheEntryTooLarge, info.Size())
	}
	data, readErr := io.ReadAll(io.LimitReader(handle, limit+1))
	closeErr := handle.Close()
	if len(data) > int(limit) {
		return nil, info, false, ErrCacheEntryTooLarge
	}
	if combined := errors.Join(readErr, closeErr); combined != nil {
		return nil, info, false, combined
	}
	return data, info, true, nil
}

// openLockedCacheLockFile 在同一句柄上完成身份复核和跨进程加锁，调用方必须负责关闭句柄。
func (c *File) openLockedCacheLockFile(path string) (*os.File, os.FileInfo, error) {
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, os.ErrNotExist
	}
	if err != nil {
		return nil, linkInfo, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return nil, linkInfo, ErrUnsafeCacheEntry
	}
	// #nosec G304 -- path 由缓存根目录和哈希文件名生成，且在句柄上再次复核身份。
	handle, err := openCacheLockFileForUpdate(path)
	if err != nil {
		return nil, linkInfo, err
	}
	if err = lockCacheLockFile(handle); err != nil {
		return nil, linkInfo, errors.Join(err, handle.Close())
	}
	info, statErr := handle.Stat()
	verifiedInfo, verifyErr := os.Lstat(path)
	if statErr != nil || verifyErr != nil || !info.Mode().IsRegular() || verifiedInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, verifiedInfo) {
		return nil, info, errors.Join(ErrUnsafeCacheEntry, statErr, verifyErr, closeLockedCacheLockFile(handle))
	}
	if info.Size() < 0 || info.Size() > maxFileLockBytes {
		return nil, info, errors.Join(fmt.Errorf("%w: %d", ErrCacheEntryTooLarge, info.Size()), closeLockedCacheLockFile(handle))
	}
	return handle, info, nil
}

// writeCacheLockPayload 在已加锁的租约句柄上原地写入完整载荷，避免路径替换造成旧 owner 覆盖新 owner。
func writeCacheLockPayload(handle *os.File, payload fileLockPayload) error {
	if handle == nil {
		return ErrInvalidCacheLock
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > maxFileLockBytes {
		return ErrCacheEntryTooLarge
	}
	if err = handle.Truncate(0); err != nil {
		return err
	}
	if _, err = handle.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = writeAll(handle, data); err != nil {
		return err
	}
	return handle.Sync()
}

// readCacheLockPayload 从已加锁句柄读取并校验租约载荷，避免读取过程中看到半写入 JSON。
func readCacheLockPayload(handle *os.File) (*fileLockPayload, error) {
	if handle == nil {
		return nil, ErrInvalidCacheLock
	}
	if _, err := handle.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(handle, maxFileLockBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileLockBytes {
		return nil, ErrCacheEntryTooLarge
	}
	var payload fileLockPayload
	if err = json.Unmarshal(data, &payload); err != nil || payload.Owner == "" || payload.Expiry.IsZero() {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
	}
	return &payload, nil
}

// closeLockedCacheLockFile 先释放 OS 文件锁，再关闭句柄，保证其他进程只会看到完整载荷。
func closeLockedCacheLockFile(handle *os.File) error {
	if handle == nil {
		return nil
	}
	return errors.Join(unlockCacheLockFile(handle), handle.Close())
}

func (c *File) cacheFilePath(key string) string {
	return filepath.Join(c.path, hashedCacheFilename(key, ".cache"))
}

func (c *File) lockFilePath(key string) string {
	return filepath.Join(c.path, hashedCacheFilename(key, ".lock"))
}

func (c *File) keyLock(key string) *sync.Mutex {
	digest := sha256.Sum256([]byte(key))
	return &c.keyLocks[int(digest[0])%len(c.keyLocks)]
}

// newFileOperationOwner 为驱动内部的计数临界区生成不可预测 owner，避免与业务锁 owner 重叠。
func newFileOperationOwner() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "cache-counter:" + hex.EncodeToString(random), nil
}

func hashedCacheFilename(key, suffix string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]) + suffix
}

func isManagedCacheFilename(name string) bool {
	if !strings.HasSuffix(name, ".cache") {
		return false
	}
	digest := strings.TrimSuffix(name, ".cache")
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func writeAll(writer io.Writer, data []byte) error {
	written, err := writer.Write(data)
	if err == nil && written != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func ignoreNotExist(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
