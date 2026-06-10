package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// File 基于文件系统实现缓存驱动，适合单机部署场景。
type File struct {
	path string
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
		_ = os.MkdirAll(path, 0o755)
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

	data, _ := json.Marshal(storedItem{
		Val:    val,
		Expiry: expiry,
	})
	_ = os.WriteFile(c.cacheFilePath(key), data, 0o644)
}

func (c *File) Has(key string) bool {
	return c.Get(key) != nil
}

func (c *File) Delete(key string) {
	_ = os.Remove(c.cacheFilePath(key))
}

func (c *File) Clear() {
	_ = os.RemoveAll(c.path)
	_ = os.MkdirAll(c.path, 0o755)
}

func (c *File) Inc(key string, step int64) int64 {
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
		handle, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
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
