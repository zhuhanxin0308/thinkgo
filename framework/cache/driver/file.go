package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// File 基于文件系统实现缓存驱动，适合单机部署场景。
type File struct {
	path  string
	incMu sync.Mutex // 保护 Inc/Dec 的读-改-写，保证进程内原子性
}

type storedItem struct {
	Val    interface{} `json:"val"`
	Expiry time.Time   `json:"expiry"`
}

type fileLockPayload struct {
	Owner  string    `json:"owner"`
	Expiry time.Time `json:"expiry"`
}

// NewFile 创建文件缓存驱动。
func NewFile(path string) *File {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = os.MkdirAll(path, 0o700)
	}
	return &File{path: path}
}

func (c *File) Get(key string) interface{} {
	data, err := os.ReadFile(c.cacheFilePath(key))
	if err != nil {
		return nil
	}

	var item storedItem
	if err := json.Unmarshal(data, &item); err != nil {
		return nil
	}

	if !item.Expiry.IsZero() && time.Now().After(item.Expiry) {
		_ = os.Remove(c.cacheFilePath(key))
		return nil
	}
	return item.Val
}

func (c *File) Set(key string, val interface{}, ttl time.Duration) {
	expiry := time.Time{}
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}

	data, err := json.Marshal(storedItem{
		Val:    val,
		Expiry: expiry,
	})
	if err != nil {
		return
	}

	// 先写临时文件再原子改名，避免并发读取到半截内容或并发写互相覆盖出损坏文件。
	target := c.cacheFilePath(key)
	tmp, err := os.CreateTemp(c.path, ".tmp-cache-*")
	if err != nil {
		_ = os.WriteFile(target, data, 0o600)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
	}
}

func (c *File) Has(key string) bool {
	return c.Get(key) != nil
}

func (c *File) Delete(key string) {
	_ = os.Remove(c.cacheFilePath(key))
}

func (c *File) Clear() {
	entries, err := os.ReadDir(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.MkdirAll(c.path, 0o700)
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !isManagedCacheFile(entry.Name()) {
			continue
		}
		_ = os.Remove(filepath.Join(c.path, entry.Name()))
	}
}

func (c *File) Inc(key string, step int64) int64 {
	// 单机文件缓存通过进程内互斥保证 Inc/Dec 原子性（跨进程仍需分布式锁）。
	c.incMu.Lock()
	defer c.incMu.Unlock()

	value := c.Get(key)
	current := int64(0)
	switch typed := value.(type) {
	case float64:
		current = int64(typed)
	case int64:
		current = typed
	case int:
		current = int64(typed)
	}

	current += step
	c.Set(key, current, 0)
	return current
}

func (c *File) Dec(key string, step int64) int64 {
	return c.Inc(key, -step)
}

// AcquireLock 通过独占创建锁文件实现跨进程锁。
func (c *File) AcquireLock(key string, owner string, ttl time.Duration) bool {
	lockFile := c.lockFilePath(key)
	now := time.Now()

	for {
		handle, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			payload, _ := json.Marshal(fileLockPayload{
				Owner:  owner,
				Expiry: now.Add(ttl),
			})
			_, _ = handle.Write(payload)
			_ = handle.Close()
			return true
		}
		if !os.IsExist(err) {
			return false
		}

		payload, readErr := c.readLockFile(lockFile)
		if readErr != nil || payload.Expiry.After(now) {
			return false
		}
		_ = os.Remove(lockFile)
	}
}

// isManagedCacheFile 只允许 Clear 删除本驱动生成的缓存、锁和未完成临时文件。
func isManagedCacheFile(name string) bool {
	return strings.HasSuffix(name, ".cache") ||
		strings.HasSuffix(name, ".lock") ||
		strings.HasPrefix(name, ".tmp-cache-")
}

// ReleaseLock 仅允许锁拥有者释放锁。
func (c *File) ReleaseLock(key string, owner string) bool {
	lockFile := c.lockFilePath(key)
	payload, err := c.readLockFile(lockFile)
	if err != nil || payload.Owner != owner {
		return false
	}
	if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
		return false
	}
	return true
}

// cacheFilePath 把任意缓存键映射到安全稳定的文件名。
func (c *File) cacheFilePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	filename := hex.EncodeToString(sum[:]) + ".cache"
	return filepath.Join(c.path, filename)
}

func (c *File) lockFilePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	filename := hex.EncodeToString(sum[:]) + ".lock"
	return filepath.Join(c.path, filename)
}

func (c *File) readLockFile(lockFile string) (*fileLockPayload, error) {
	data, err := os.ReadFile(lockFile)
	if err != nil {
		return nil, err
	}
	var payload fileLockPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}
