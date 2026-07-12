package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		remove := strings.HasSuffix(name, ".cache")
		if strings.HasPrefix(name, ".tmp-cache-") {
			if info, infoErr := entry.Info(); infoErr == nil && now.Sub(info.ModTime()) >= staleTempFileAge {
				remove = true
			}
		}
		if remove {
			resultErr = errors.Join(resultErr, ignoreNotExist(os.Remove(filepath.Join(c.path, name))))
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

func (c *File) changeCounter(key string, step int64, subtract bool) (int64, error) {
	if c == nil || c.path == "" {
		return 0, ErrInvalidCachePath
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
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
		handle, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			payload, marshalErr := json.Marshal(fileLockPayload{Owner: owner, Expiry: now.Add(ttl)})
			writeErr := error(nil)
			if marshalErr == nil {
				writeErr = writeAll(handle, payload)
				if writeErr == nil {
					writeErr = handle.Sync()
				}
			}
			closeErr := handle.Close()
			if combined := errors.Join(marshalErr, writeErr, closeErr); combined != nil {
				_ = os.Remove(lockFile)
				return false, combined
			}
			return true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}

		payload, info, readErr := c.readLockFile(lockFile)
		if readErr != nil {
			if info != nil && now.Sub(info.ModTime()) >= corruptLockRecoveryAge {
				removed, removeErr := removeFileIfSame(lockFile, info)
				if removeErr != nil {
					return false, removeErr
				}
				if removed {
					continue
				}
			}
			return false, fmt.Errorf("%w: %v", ErrInvalidCacheLock, readErr)
		}
		if payload.Expiry.After(now) {
			return false, nil
		}
		removed, removeErr := removeFileIfSame(lockFile, info)
		if removeErr != nil {
			return false, removeErr
		}
		if !removed {
			return false, nil
		}
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
	payload, info, err := c.readLockFile(lockFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if payload.Owner != owner || !payload.Expiry.After(time.Now()) {
		return false, nil
	}
	return removeFileIfSame(lockFile, info)
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
	handle, err := os.Open(path)
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

func (c *File) readLockFile(path string) (*fileLockPayload, os.FileInfo, error) {
	data, info, found, err := c.readManagedFile(path, maxFileLockBytes)
	if err != nil || !found {
		return nil, info, err
	}
	var payload fileLockPayload
	if err = json.Unmarshal(data, &payload); err != nil || payload.Owner == "" || payload.Expiry.IsZero() {
		return nil, info, fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
	}
	return &payload, info, nil
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

func hashedCacheFilename(key, suffix string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]) + suffix
}

func writeAll(writer io.Writer, data []byte) error {
	written, err := writer.Write(data)
	if err == nil && written != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func removeFileIfSame(path string, expected os.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if expected == nil || !os.SameFile(expected, current) {
		return false, nil
	}
	if err = os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func ignoreNotExist(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
