package driver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClearExpired 只回收已过期的受管缓存文件，保留失效代际、租约与非缓存数据。
func (c *File) ClearExpired() error {
	if c == nil || c.path == "" {
		return ErrInvalidCachePath
	}
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	return c.withAllMutationGuards(context.Background(), func() error {
		entries, err := os.ReadDir(c.path)
		if err != nil {
			return err
		}
		now := time.Now()
		var resultErr error
		for _, entry := range entries {
			if entry.IsDir() || !isManagedCacheFilename(entry.Name()) {
				continue
			}
			path := filepath.Join(c.path, entry.Name())
			data, identity, found, err := c.readManagedFile(path, maxFileCacheEntryBytes)
			if err != nil || !found {
				resultErr = errors.Join(resultErr, err)
				continue
			}
			var item storedItem
			if err := json.Unmarshal(data, &item); err != nil || len(item.Value) == 0 {
				// 无法识别的文件不会作为过期缓存删除。
				continue
			}
			if strings.HasPrefix(item.Key, cacheFenceMetadataPrefix) || item.Expiry.IsZero() || now.Before(item.Expiry) {
				continue
			}
			_, err = removeFileIfSame(path, identity)
			resultErr = errors.Join(resultErr, ignoreNotExist(err))
		}
		return resultErr
	})
}
